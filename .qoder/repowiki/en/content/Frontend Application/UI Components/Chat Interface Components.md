# Chat Interface Components

<cite>
**Referenced Files in This Document**
- [AssistantMessage.tsx](file://frontend/src/components/chat/AssistantMessage.tsx)
- [UserMessage.tsx](file://frontend/src/components/chat/UserMessage.tsx)
- [ChatInput.tsx](file://frontend/src/components/chat/ChatInput.tsx)
- [ChatArea.tsx](file://frontend/src/components/chat/ChatArea.tsx)
- [ChatMessageRenderer.tsx](file://frontend/src/components/chat/ChatMessageRenderer.tsx)
- [PlanView.tsx](file://frontend/src/components/chat/PlanView.tsx)
- [DAGGraph.tsx](file://frontend/src/components/chat/DAGGraph.tsx)
- [ExecutionPanels.tsx](file://frontend/src/components/chat/ExecutionPanels.tsx)
- [ThoughtBlock.tsx](file://frontend/src/components/chat/ThoughtBlock.tsx)
- [ReflectionBlock.tsx](file://frontend/src/components/chat/ReflectionBlock.tsx)
- [ToolBlock.tsx](file://frontend/src/components/chat/ToolBlock.tsx)
- [PlanStepBlock.tsx](file://frontend/src/components/chat/PlanStepBlock.tsx)
- [ThoughtGroupBlock.tsx](file://frontend/src/components/chat/ThoughtGroupBlock.tsx)
- [CollapsibleBlock.tsx](file://frontend/src/components/chat/CollapsibleBlock.tsx)
- [ScrollContext.tsx](file://frontend/src/components/chat/ScrollContext.tsx)
- [ToolContentBlock.tsx](file://frontend/src/components/chat/ToolContentBlock.tsx)
- [BlackboardPanel.tsx](file://frontend/src/components/chat/BlackboardPanel.tsx)
- [useMessageSender.ts](file://frontend/src/hooks/useMessageSender.ts)
- [chatUtils.ts](file://frontend/src/lib/chatUtils.ts)
- [chatUtilsHelpers.ts](file://frontend/src/lib/chatUtilsHelpers.ts)
- [chatGroupingHandlers.ts](file://frontend/src/lib/chatGroupingHandlers.ts)
- [planStore.ts](file://frontend/src/stores/planStore.ts)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)
- [panelStore.ts](file://frontend/src/stores/panelStore.ts)
- [blackboardStore.ts](file://frontend/src/stores/blackboardStore.ts)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [dagLayout.ts](file://frontend/src/lib/dagLayout.ts)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)
- [chat.ts](file://frontend/src/api/chat.ts)
- [sessions.ts](file://frontend/src/api/sessions.ts)
- [useBlackboardEvents.ts](file://frontend/src/hooks/events/useBlackboardEvents.ts)
- [messages.ts](file://frontend/src/types/messages.ts)
- [models.ts](file://frontend/src/types/models.ts)
</cite>

## Update Summary
**Changes Made**
- Added comprehensive documentation for the new useMessageSender hook that encapsulates complex session creation, message sending, and cancellation handling
- Updated ChatInput component documentation to reflect its streamlined implementation using the new hook
- Enhanced metadata typing documentation with improved structured types for action resolutions
- Updated component architecture to reflect the new centralized message sending flow

## Table of Contents
1. [Introduction](#introduction)
2. [Project Structure](#project-structure)
3. [Core Components](#core-components)
4. [Architecture Overview](#architecture-overview)
5. [Detailed Component Analysis](#detailed-component-analysis)
6. [Dependency Analysis](#dependency-analysis)
7. [Performance Considerations](#performance-considerations)
8. [Accessibility Features](#accessibility-features)
9. [Customization Options](#customization-options)
10. [Troubleshooting Guide](#troubleshooting-guide)
11. [Conclusion](#conclusion)

## Introduction
This document provides comprehensive documentation for C0WRK's chat interface components. It covers the core messaging components (AssistantMessage, UserMessage, ChatInput), specialized execution visualization components (PlanView, DAGGraph, ExecutionPanels), and the rendering pipeline (ChatArea, ChatMessageRenderer) along with specialized blocks (ThoughtBlock, ReflectionBlock, ToolBlock). The system has been completely rewritten with a new plan-based architecture featuring CollapsibleBlock.tsx, ScrollContext.tsx, and ToolContentBlock.tsx, providing enhanced message grouping capabilities and improved component composition patterns.

**Updated** The interface now includes a centralized useMessageSender hook that encapsulates complex session creation, message sending, and cancellation handling, significantly reducing code duplication and improving error handling consistency across the chat interface.

## Project Structure
The chat UI is organized under frontend/src/components/chat with supporting libraries and stores under frontend/src/lib and frontend/src/stores respectively. The key areas are:
- Messaging and input: AssistantMessage, UserMessage, ChatInput
- Rendering pipeline: ChatArea, ChatMessageRenderer
- Specialized blocks: ThoughtBlock, ReflectionBlock, ToolBlock, ThoughtGroupBlock, PlanStepBlock
- Foundation components: CollapsibleBlock, ToolContentBlock, ScrollContext
- Execution visualization: PlanView, DAGGraph, ExecutionPanels
- Real-time monitoring: BlackboardPanel
- **New** Centralized message handling: useMessageSender hook
- Stores: chatStore (message grouping and UI state), planStore (execution plan state), blackboardStore (real-time execution state), sessionStore (session management)
- Utilities: markdownConfig (ReactMarkdown configuration), chatUtils (advanced history conversion), chatUtilsHelpers (message grouping helpers), chatGroupingHandlers (specialized handlers)
- APIs: chat.ts (message sending and cancellation), sessions.ts (session management)

```mermaid
graph TB
subgraph "Chat UI"
CA["ChatArea"]
CMR["ChatMessageRenderer"]
AM["AssistantMessage"]
UM["UserMessage"]
CI["ChatInput"]
PB["PlanView"]
DG["DAGGraph"]
EP["ExecutionPanels"]
BB["BlackboardPanel"]
TB["ToolBlock"]
THB["ThoughtBlock"]
RFB["ReflectionBlock"]
PSB["PlanStepBlock"]
TGB["ThoughtGroupBlock"]
CB["CollapsibleBlock"]
TCB["ToolContentBlock"]
SC["ScrollContext"]
UMS["useMessageSender"]
end
subgraph "Stores"
CS["chatStore"]
PS["planStore"]
BS["blackboardStore"]
SS["sessionStore"]
end
subgraph "Libraries"
MC["markdownConfig"]
DU["dagLayout"]
CU["chatUtils"]
CUH["chatUtilsHelpers"]
CGH["chatGroupingHandlers"]
end
subgraph "API Layer"
CHAT["chat API"]
SESS["sessions API"]
BBE["useBlackboardEvents"]
end
CA --> CMR
CMR --> AM
CMR --> UM
CMR --> TB
CMR --> THB
CMR --> RFB
CMR --> PSB
CMR --> TGB
EP --> DG
PB --> PS
PS --> PSB
DG --> DU
CA --> CS
EP --> PS
BB --> BS
BS --> BBE
AM --> MC
CI --> UMS
CI --> CS
CI --> SS
UMS --> CHAT
UMS --> SESS
CS --> CU
CU --> CUH
CU --> CGH
```

**Diagram sources**
- [ChatArea.tsx:17-146](file://frontend/src/components/chat/ChatArea.tsx#L17-L146)
- [ChatMessageRenderer.tsx:1-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L1-L126)
- [AssistantMessage.tsx:25-90](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L90)
- [UserMessage.tsx:10-104](file://frontend/src/components/chat/UserMessage.tsx#L10-L104)
- [ChatInput.tsx:13-241](file://frontend/src/components/chat/ChatInput.tsx#L13-L241)
- [PlanView.tsx:1-65](file://frontend/src/components/chat/PlanView.tsx#L1-L65)
- [DAGGraph.tsx:13-88](file://frontend/src/components/chat/DAGGraph.tsx#L13-L88)
- [ExecutionPanels.tsx:1-61](file://frontend/src/components/chat/ExecutionPanels.tsx#L1-L61)
- [BlackboardPanel.tsx:10-52](file://frontend/src/components/chat/BlackboardPanel.tsx#L10-L52)
- [ToolBlock.tsx:1-38](file://frontend/src/components/chat/ToolBlock.tsx#L1-L38)
- [ThoughtBlock.tsx:1-45](file://frontend/src/components/chat/ThoughtBlock.tsx#L1-L45)
- [ReflectionBlock.tsx:1-63](file://frontend/src/components/chat/ReflectionBlock.tsx#L1-L63)
- [PlanStepBlock.tsx:1-78](file://frontend/src/components/chat/PlanStepBlock.tsx#L1-L78)
- [ThoughtGroupBlock.tsx:13-45](file://frontend/src/components/chat/ThoughtGroupBlock.tsx#L13-L45)
- [CollapsibleBlock.tsx:1-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L1-L67)
- [ToolContentBlock.tsx:1-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L1-L78)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [sessionStore.ts:1-76](file://frontend/src/stores/sessionStore.ts#L1-L76)
- [markdownConfig.tsx:27-77](file://frontend/src/lib/markdownConfig.tsx#L27-L77)
- [dagLayout.ts:33-237](file://frontend/src/lib/dagLayout.ts#L33-L237)
- [chatUtils.ts:1-176](file://frontend/src/lib/chatUtils.ts#L1-L176)
- [chatUtilsHelpers.ts:1-186](file://frontend/src/lib/chatUtilsHelpers.ts#L1-L186)
- [chatGroupingHandlers.ts:1-158](file://frontend/src/lib/chatGroupingHandlers.ts#L1-L158)
- [chat.ts:1-56](file://frontend/src/api/chat.ts#L1-L56)
- [sessions.ts:1-56](file://frontend/src/api/sessions.ts#L1-L56)
- [useBlackboardEvents.ts:1-59](file://frontend/src/hooks/events/useBlackboardEvents.ts#L1-L59)
- [messages.ts:1-125](file://frontend/src/types/messages.ts#L1-L125)

**Section sources**
- [ChatArea.tsx:17-146](file://frontend/src/components/chat/ChatArea.tsx#L17-L146)
- [ChatMessageRenderer.tsx:1-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L1-L126)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [sessionStore.ts:1-76](file://frontend/src/stores/sessionStore.ts#L1-L76)

## Core Components
This section documents the primary chat components and their responsibilities, now built on the new foundation architecture with centralized message handling.

- AssistantMessage
  - Purpose: Renders assistant messages with optional raw/source toggle and streaming cursor.
  - Props: content (string), isStreaming (boolean, optional).
  - Rendering logic: Uses ReactMarkdown with plugins for GFM, emoji, breaks, syntax highlighting, sanitization, external links, slugs, and autolinks. Provides a toggle to view raw Markdown with highlight.js. Includes an ErrorBoundary around the renderer.
  - Accessibility: Hover-triggered button with aria-label; stream cursor is visually indicated.
  - Customization: Uses markdownComponents and customSchema from markdownConfig.

- UserMessage
  - Purpose: Renders user messages with timestamp and optional pinning behavior.
  - Props: content (string), timestamp (number), isPinned (boolean, optional), maxHeight (number, optional).
  - Rendering logic: Non-pinned renders a standard bubble; pinned messages can overflow with a gradient mask and expand/collapse behavior controlled by ResizeObserver and keyboard focus.
  - Accessibility: Proper roles, tabindex, aria-expanded for clipped pinned messages; focus blur handling to collapse.

- ChatInput
  - Purpose: Text input for user messages with auto-resize, send/cancel controls, and optimistic UI updates.
  - Props: None (uses stores/hooks internally).
  - **Updated** Implementation: Now uses the useMessageSender hook for all message sending operations, significantly reducing code duplication and centralizing error handling.
  - Interaction pattern: Optimistically adds user message; creates session if missing; marks task active; sends via Wails API; handles cancellation; disables input when blocked; shows blocking message.
  - State management: Uses sessionStore, projectStore, chatStore, and the new useMessageSender hook; manages textarea height and placeholder text dynamically.

**Updated** useMessageSender Hook
  - Purpose: Centralized message sending flow that encapsulates session creation, optimistic UI updates, backend RPC calls, and error handling.
  - Props: None (returns send, cancel, and isProcessing functions).
  - Features: Automatic session creation when none exists, optimistic UI message addition, centralized error handling, and consistent state management across all message operations.
  - Benefits: Removes over 50 lines of duplicated code from ChatInput, provides consistent error handling, and improves maintainability.

**Updated** Enhanced Metadata Typing
  - Purpose: Structured types for action resolution metadata with improved type safety.
  - Features: Typed resolution metadata for tool confirmations, step limits, ask user responses, and resume actions.
  - Benefits: Better compile-time type checking, improved developer experience, and more reliable action resolution handling.

**Section sources**
- [AssistantMessage.tsx:20-90](file://frontend/src/components/chat/AssistantMessage.tsx#L20-L90)
- [markdownConfig.tsx:27-77](file://frontend/src/lib/markdownConfig.tsx#L27-L77)
- [UserMessage.tsx:3-104](file://frontend/src/components/chat/UserMessage.tsx#L3-L104)
- [ChatInput.tsx:13-241](file://frontend/src/components/chat/ChatInput.tsx#L13-L241)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)
- [messages.ts:55-125](file://frontend/src/types/messages.ts#L55-L125)

## Architecture Overview
The chat architecture centers on a completely rewritten rendering pipeline that transforms backend messages into a structured display model using advanced grouping capabilities, then rendered by ChatMessageRenderer. The new plan-based architecture maintains execution plan state in planStore and visualizes it via PlanView and DAGGraph. ChatArea orchestrates history loading, pinned user message display, scrolling, and integrates with session events through the new component foundation.

**Updated** The architecture now includes a centralized useMessageSender hook that provides a unified interface for all message-related operations, significantly improving code organization and maintainability.

```mermaid
sequenceDiagram
participant User as "User"
participant Input as "ChatInput"
participant Sender as "useMessageSender"
participant Session as "sessionStore"
participant Chat as "chatStore"
participant Plan as "planStore"
participant Blackboard as "blackboardStore"
participant Backend as "Wails API"
participant Area as "ChatArea"
participant Utils as "chatUtils"
participant Renderer as "ChatMessageRenderer"
User->>Input : Type message and press Enter
Input->>Sender : send(messageText)
Sender->>Session : Get activeSessionId
alt No session
Sender->>Backend : CreateSession()
Backend-->>Sender : New session
Sender->>Session : addSession(), setActiveSession()
end
Sender->>Chat : addMessage(user)
Sender->>Chat : setTaskActive(true)
Sender->>Backend : SendMessage(sessionId, text)
Backend-->>Chat : Stream tokens (setStreaming/appendStreamToken)
Backend-->>Plan : Events (plan, plan_step_start/complete)
Backend-->>Blackboard : Events (blackboard_updated)
Area->>Backend : GetSessionHistory(sessionId)
Backend-->>Area : History messages
Area->>Chat : setMessages(sessionId, uiMessages)
Area->>Utils : rebuildPlanFromHistory(uiMessages)
Utils->>Plan : setPlan(group)
Area->>Utils : groupMessages(uiMessages)
Utils->>Plan : handlePlanStepStart/Complete
Utils->>Chat : handleToolCall/Result
Utils->>Chat : handleActionMessage
Utils->>Chat : handleStepFinish
Renderer->>Chat : Read messages/streamingText
Renderer-->>User : Rendered UI
```

**Diagram sources**
- [ChatInput.tsx:48-58](file://frontend/src/components/chat/ChatInput.tsx#L48-L58)
- [useMessageSender.ts:24-70](file://frontend/src/hooks/useMessageSender.ts#L24-L70)
- [ChatArea.tsx:48-73](file://frontend/src/components/chat/ChatArea.tsx#L48-L73)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)
- [blackboardStore.ts:44-53](file://frontend/src/stores/blackboardStore.ts#L44-L53)
- [ChatMessageRenderer.tsx:111-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L111-L126)
- [chatUtils.ts:117-175](file://frontend/src/lib/chatUtils.ts#L117-L175)

## Detailed Component Analysis

### Foundation Components

#### CollapsibleBlock Analysis
- Purpose: Universal collapsible container providing consistent UI patterns across all collapsible components.
- Props: icon, label, statusIcon, badge, defaultOpen, open, onOpenChange, className, children, headerExtra.
- Behavior: Supports both controlled and uncontrolled modes, hover-triggered chevron indicators, and flexible header layouts.
- Integration: Used as the base for ThoughtBlock, ReflectionBlock, ToolBlock, and PlanStepBlock.

```mermaid
flowchart TD
Start(["CollapsibleBlock"]) --> Mode{"Controlled/Uncontrolled"}
Mode --> |Controlled| Controlled["Use provided open/onOpenChange"]
Mode --> |Uncontrolled| Uncontrolled["Use internal state"]
Controlled --> Render["Render with provided props"]
Uncontrolled --> Render
Render --> Header["CollapsibleTrigger with icons"]
Header --> Chevron{"Hover?"}
Chevron --> |Yes| ShowChevron["Show chevron indicator"]
Chevron --> |No| HideChevron["Hide chevron"]
ShowChevron --> Content["CollapsibleContent"]
HideChevron --> Content
Content --> Children["Render children"]
Children --> End(["Done"])
```

**Diagram sources**
- [CollapsibleBlock.tsx:23-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L23-L67)

**Section sources**
- [CollapsibleBlock.tsx:10-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L10-L67)

#### ScrollContext Analysis
- Purpose: Provides global scroll-to-step functionality for coordinating navigation between plan view and execution details.
- Props: None (provides context to children).
- Functionality: Maintains a callback reference that can be triggered from anywhere in the component tree.
- Integration: Used by PlanView to enable step navigation from plan items.

```mermaid
flowchart TD
Start(["ScrollContext"]) --> Provider["ScrollProvider"]
Provider --> Ref["Store callback in ref"]
Ref --> Expose["Expose scrollToStep/setScrollToStep"]
Expose --> Consumer["useScrollContext()"]
Consumer --> UseCallback["Use stored callback"]
UseCallback --> End(["Navigation"])
```

**Diagram sources**
- [ScrollContext.tsx:12-37](file://frontend/src/components/chat/ScrollContext.tsx#L12-L37)

**Section sources**
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)

#### ToolContentBlock Analysis
- Purpose: Unified display component for tool arguments and results with intelligent preview and expand/collapse functionality.
- Props: args (string), result (string, optional), resultLen (number, optional), borderClass (string, optional).
- Features: Automatic truncation at 200 characters, show-more toggle, result length formatting (K chars), and consistent styling.
- Integration: Used by ToolBlock and MemoryBlock for consistent tool display.

```mermaid
flowchart TD
Start(["ToolContentBlock"]) --> CheckArgs{"Args > 200 chars?"}
CheckArgs --> |Yes| TruncateArgs["Truncate args + '...'"]
CheckArgs --> |No| UseArgs["Use full args"]
CheckResult{"Result exists?"}
CheckResult --> |Yes| CheckResultLen{"Result > 200 chars?"}
CheckResultLen --> |Yes| TruncateResult["Truncate result + '...'"]
CheckResultLen --> |No| UseResult["Use full result"]
CheckResult --> |No| NoResult["No result display"]
TruncateArgs --> HasLong{"Has long content?"}
UseArgs --> HasLong
TruncateResult --> HasLong
UseResult --> HasLong
NoResult --> HasLong
HasLong --> |Yes| ShowToggle["Show 'Show more' toggle"]
HasLong --> |No| Render["Render content"]
ShowToggle --> Toggle["Expand/collapse state"]
Toggle --> Render
Render --> End(["Done"])
```

**Diagram sources**
- [ToolContentBlock.tsx:35-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L35-L78)

**Section sources**
- [ToolContentBlock.tsx:27-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L27-L78)

### AssistantMessage Analysis
- Props: content (string), isStreaming (boolean).
- Rendering: Two modes—rendered via ReactMarkdown with plugins and components, or raw Markdown with highlight.js. Toggle button appears only when not streaming.
- Security: Sanitization via customSchema; external links configured; slug and autolink headings supported.
- Error handling: ErrorBoundary wraps the renderer to avoid crashing the chat.

```mermaid
flowchart TD
Start(["Render AssistantMessage"]) --> CheckStreaming{"isStreaming?"}
CheckStreaming --> |Yes| ShowCursor["Show inline cursor"]
CheckStreaming --> |No| CheckMode{"showRaw?"}
CheckMode --> |Yes| RawMode["Preformatted raw Markdown<br/>with highlight.js"]
CheckMode --> |No| RenderedMode["ReactMarkdown with plugins<br/>and custom components"]
RenderedMode --> End(["Done"])
RawMode --> End
ShowCursor --> End
```

**Diagram sources**
- [AssistantMessage.tsx:25-90](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L90)
- [markdownConfig.tsx:27-77](file://frontend/src/lib/markdownConfig.tsx#L27-L77)

**Section sources**
- [AssistantMessage.tsx:25-90](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L90)
- [markdownConfig.tsx:27-77](file://frontend/src/lib/markdownConfig.tsx#L27-L77)

### UserMessage Analysis
- Props: content, timestamp, isPinned, maxHeight.
- Behavior: Measures natural height for pinned messages; collapses with gradient mask when exceeding maxHeight; expands on click/tap; collapses on blur.
- Accessibility: Focus management, aria-expanded, role="button" when overflow occurs.

```mermaid
flowchart TD
Start(["Render UserMessage"]) --> IsPinned{"isPinned?"}
IsPinned --> |No| Standard["Standard right-aligned bubble"]
IsPinned --> |Yes| Measure["Measure natural height via ResizeObserver"]
Measure --> Overflow{"naturalHeight > maxHeight?"}
Overflow --> |No| RenderBubble["Render bubble"]
Overflow --> |Yes| Clip["Render with gradient mask"]
Clip --> Click{"Click?"}
Click --> |Yes| ToggleExpand["Toggle expanded state"]
ToggleExpand --> RenderBubble
RenderBubble --> End(["Done"])
```

**Diagram sources**
- [UserMessage.tsx:10-104](file://frontend/src/components/chat/UserMessage.tsx#L10-L104)

**Section sources**
- [UserMessage.tsx:10-104](file://frontend/src/components/chat/UserMessage.tsx#L10-L104)

### ChatInput Analysis
- Props: None.
- State: text, isOptimizing, isProcessing.
- **Updated** Implementation: Now uses the useMessageSender hook for all message sending operations, significantly reducing code duplication and centralizing error handling.
- Flow: Validates project/session, optimistically adds user message, sets task active, calls API, handles errors, resets processing state, adjusts textarea height.
- Controls: Send button (green) and Cancel button (red) depending on task state.

```mermaid
sequenceDiagram
participant U as "User"
participant CI as "ChatInput"
participant UMS as "useMessageSender"
participant SS as "sessionStore"
participant CS as "chatStore"
participant API as "Wails API"
U->>CI : Type text
U->>CI : Press Enter
CI->>UMS : send(messageText)
UMS->>SS : activeSessionId
alt No session
UMS->>API : CreateSession()
API-->>UMS : session
UMS->>SS : addSession(), setActiveSession()
end
UMS->>CS : addMessage(user)
UMS->>CS : setTaskActive(true)
UMS->>API : SendMessage(sessionId, text)
API-->>CI : Success/Failure
alt Failure
UMS->>CS : addMessage(error)
UMS->>CS : setTaskActive(false)
end
```

**Diagram sources**
- [ChatInput.tsx:36-58](file://frontend/src/components/chat/ChatInput.tsx#L36-L58)
- [useMessageSender.ts:24-70](file://frontend/src/hooks/useMessageSender.ts#L24-L70)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)

**Section sources**
- [ChatInput.tsx:13-241](file://frontend/src/components/chat/ChatInput.tsx#L13-L241)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)

### useMessageSender Hook Analysis
- Purpose: Centralized message sending flow that encapsulates session creation, optimistic UI updates, backend RPC calls, and error handling.
- Props: None (returns send, cancel, and isProcessing functions).
- Implementation: Uses Zustand stores directly for state management, providing a clean separation between UI logic and business logic.
- Features: Automatic session creation when none exists, optimistic UI message addition, centralized error handling, and consistent state management across all message operations.
- Benefits: Removes over 50 lines of duplicated code from ChatInput, provides consistent error handling, and improves maintainability.

```mermaid
flowchart TD
Start(["useMessageSender"]) --> State["Initialize isProcessing state"]
State --> Send["send(messageText)"]
Send --> Validate{"messageText.trim()?"}
Validate --> |No| Return["Return early"]
Validate --> |Yes| SetProcessing["setIsProcessing(true)"]
SetProcessing --> CheckSession{"activeSessionId?"}
CheckSession --> |No| CreateSession["createSession()"]
CreateSession --> AddSession["addSession(), setActiveSession()"]
AddSession --> SetSessionId["sessionId = newSession.id"]
CheckSession --> |Yes| SetSessionId
SetSessionId --> AddMessage["addMessage(user)"]
AddMessage --> TouchSession["touchSession()"]
TouchSession --> SetTaskActive["setTaskActive(true)"]
SetTaskActive --> SetActivity["setActivityStatus('Processing...')"]
SetActivity --> SendMessage["sendMessage(sessionId, messageText)"]
SendMessage --> HandleError{"Error?"}
HandleError --> |Yes| LogError["logger.error()"]
LogError --> AddErrorMessage["addMessage(error)"]
AddErrorMessage --> ResetTask["setTaskActive(false)"]
ResetTask --> SetProcessingFalse["setIsProcessing(false)"]
HandleError --> |No| SetProcessingFalse
SetProcessingFalse --> End(["Done"])
Return --> End
```

**Diagram sources**
- [useMessageSender.ts:21-83](file://frontend/src/hooks/useMessageSender.ts#L21-L83)

**Section sources**
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)

### ChatArea Analysis
- Props: None.
- Responsibilities: Loads session history, groups messages using advanced grouping capabilities, pins the last user message, manages container height for pinned clipping, subscribes to session events, clears plan store when no session, and integrates BlackboardPanel for real-time execution monitoring.
- Integration: Uses chatStore for messages/streamingText, planStore for plan state, blackboardStore for real-time execution state, and chatUtils for history conversion and plan rebuilding.

**Updated** BlackboardPanel Integration
- The BlackboardPanel is rendered twice in the ChatArea component - once in the empty state (lines 117-118) and once in the full content state (line 142).
- It automatically adapts to sidebar and file viewer panel states for proper spacing.
- Only renders when there is blackboard state available and an active session exists.

```mermaid
flowchart TD
Start(["ChatArea mount"]) --> HasSession{"activeSessionId?"}
HasSession --> |No| ClearPlan["Clear plan store"]
ClearPlan --> Empty["Show empty state<br/>with BlackboardPanel"]
HasSession --> |Yes| LoadHistory["GetSessionHistory()"]
LoadHistory --> Convert["chatMessageToUI()"]
Convert --> SetMsgs["setMessages()"]
SetMsgs --> RebuildPlan["rebuildPlanFromHistory()"]
RebuildPlan --> Group["groupMessages()"]
Group --> PinLast["Find last user message"]
PinLast --> Render["Render pinned + scrollable chat<br/>with BlackboardPanel"]
```

**Diagram sources**
- [ChatArea.tsx:21-146](file://frontend/src/components/chat/ChatArea.tsx#L21-L146)
- [chatUtils.ts:117-175](file://frontend/src/lib/chatUtils.ts#L117-L175)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)

**Section sources**
- [ChatArea.tsx:21-146](file://frontend/src/components/chat/ChatArea.tsx#L21-L146)
- [chatUtils.ts:117-175](file://frontend/src/lib/chatUtils.ts#L117-L175)

### ChatMessageRenderer Analysis
- Props: items (DisplayItem[]).
- Composition: Routes DisplayItem kinds to appropriate subcomponents using a centralized registry. Now uses CollapsibleBlock as the foundation for all collapsible components.
- Specialized blocks:
  - ThoughtBlock: Collapsible reasoning with show more/less using CollapsibleBlock.
  - ReflectionBlock: Collapsible reflection summary with suggested action badges and details using CollapsibleBlock.
  - ToolBlock: Collapsible tool invocation/results with ToolContentBlock for unified display.
  - PlanStepBlock: Collapsible step with status, duration, error, and nested children rendering using CollapsibleBlock.
  - MemoryBlock: Specialized memory read block using CollapsibleBlock and ToolContentBlock.

```mermaid
classDiagram
class ChatMessageRenderer {
+items : DisplayItem[]
+render()
}
class ThoughtBlock
class ReflectionBlock
class ToolBlock
class PlanStepBlock
class MemoryBlock
class CollapsibleBlock
class ToolContentBlock
ChatMessageRenderer --> ThoughtBlock : "renders"
ChatMessageRenderer --> ReflectionBlock : "renders"
ChatMessageRenderer --> ToolBlock : "renders"
ChatMessageRenderer --> PlanStepBlock : "renders"
ChatMessageRenderer --> MemoryBlock : "renders"
ThoughtBlock --> CollapsibleBlock : "extends"
ReflectionBlock --> CollapsibleBlock : "extends"
ToolBlock --> CollapsibleBlock : "extends"
PlanStepBlock --> CollapsibleBlock : "extends"
ToolBlock --> ToolContentBlock : "uses"
MemoryBlock --> CollapsibleBlock : "extends"
MemoryBlock --> ToolContentBlock : "uses"
```

**Diagram sources**
- [ChatMessageRenderer.tsx:75-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L75-L126)
- [ThoughtBlock.tsx:10-45](file://frontend/src/components/chat/ThoughtBlock.tsx#L10-L45)
- [ReflectionBlock.tsx:13-63](file://frontend/src/components/chat/ReflectionBlock.tsx#L13-L63)
- [ToolBlock.tsx:16-38](file://frontend/src/components/chat/ToolBlock.tsx#L16-L38)
- [PlanStepBlock.tsx:17-78](file://frontend/src/components/chat/PlanStepBlock.tsx#L17-L78)
- [MemoryBlock.tsx:52-74](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L52-L74)
- [CollapsibleBlock.tsx:23-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L23-L67)
- [ToolContentBlock.tsx:35-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L35-L78)

**Section sources**
- [ChatMessageRenderer.tsx:75-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L75-L126)

### PlanView Analysis
- Purpose: Displays the latest execution plan as a list of steps with status, duration, and optional details.
- Data: Reads from planStore.planGroups[0] and maps to view model (PlanItem[]).
- Interactions: Clickable step items that trigger scroll-to-step functionality via ScrollContext; derived labels from summary or description; status icons and badges.

```mermaid
flowchart TD
Start(["PlanView"]) --> Latest{"Has plan?"}
Latest --> |No| Empty["No plan generated message"]
Latest --> |Yes| Items["Map planGroups[0].items"]
Items --> Render["Render PlanStepItem list"]
Render --> Click{"User clicks step?"}
Click --> |Yes| Scroll["scrollToStep(stepId)"]
Click --> |No| End(["Done"])
Scroll --> End
```

**Diagram sources**
- [PlanView.tsx:47-65](file://frontend/src/components/chat/PlanView.tsx#L47-L65)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)
- [ScrollContext.tsx:32-37](file://frontend/src/components/chat/ScrollContext.tsx#L32-L37)

**Section sources**
- [PlanView.tsx:1-65](file://frontend/src/components/chat/PlanView.tsx#L1-L65)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)

### DAGGraph Analysis
- Purpose: Visualizes task dependencies as a DAG using SVG connectors.
- Data: Accepts items (PlanItem[]) with dependsOn edges.
- Layout: computeDAGLayout determines lanes and connectors; DAGGraph renders SVG lines/paths and circles.

```mermaid
flowchart TD
Start(["DAGGraph"]) --> Layout["computeDAGLayout(items)"]
Layout --> Nodes["nodes (lanes)"]
Layout --> Connectors["connectors (vertical/fork/merge)"]
Nodes --> Render["Render SVG nodes"]
Connectors --> Render
```

**Diagram sources**
- [DAGGraph.tsx:13-88](file://frontend/src/components/chat/DAGGraph.tsx#L13-L88)
- [dagLayout.ts:33-237](file://frontend/src/lib/dagLayout.ts#L33-L237)

**Section sources**
- [DAGGraph.tsx:13-88](file://frontend/src/components/chat/DAGGraph.tsx#L13-L88)
- [dagLayout.ts:33-237](file://frontend/src/lib/dagLayout.ts#L33-L237)

### ExecutionPanels Analysis
- Purpose: Collapsible panel showing execution plan with DAG visualization and step list.
- Data: Uses planStore.planGroups; integrates with ScrollContext to jump to steps.
- UI: PanelHeader with counters; PlanContent renders DAGGraph plus step list with status icons and tooltips.

```mermaid
flowchart TD
Start(["ExecutionPanels"]) --> HasPlan{"planGroups.length > 0 && activeSessionId?"}
HasPlan --> |No| Null["Do not render"]
HasPlan --> |Yes| Header["PanelHeader with counters"]
Header --> Content["PlanContent with DAGGraph + steps"]
Content --> End(["Rendered"])
```

**Diagram sources**
- [ExecutionPanels.tsx:11-61](file://frontend/src/components/chat/ExecutionPanels.tsx#L11-L61)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)

**Section sources**
- [ExecutionPanels.tsx:1-61](file://frontend/src/components/chat/ExecutionPanels.tsx#L1-L61)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)

### BlackboardPanel Analysis
- Purpose: Provides real-time visibility into agent task execution with debounced state updates, search functionality, and badge indicators.
- Props: None (uses stores/hooks internally).
- State Management: Integrates with blackboardStore for state management and useBlackboardEvents for event-driven updates.
- Features:
  - Collapsible interface with expand/collapse toggle using hover indicators
  - Badge system showing counts for plan steps, results, facts, and reflections
  - Search functionality with debounced filtering across all blackboard elements
  - Responsive design that adapts to sidebar and file viewer panel states
  - Automatic loading states and error handling

**Updated** Component Structure
- BlackboardBadges: Displays badge indicators for plan steps, results, facts, and reflections
- SearchBar: Provides search functionality with icon and input field
- BlackboardContent: Main content area with collapsible sections for different blackboard elements
- CollapsibleSection: Individual collapsible sections for plan, step results, facts, and reflections

```mermaid
flowchart TD
Start(["BlackboardPanel"]) --> CheckState{"hasBB && activeSessionId && bbState?"}
CheckState --> |No| Null["Return null (do not render)"]
CheckState --> |Yes| Container["Main container with adaptive margins"]
Container --> Header["Header with expand/collapse toggle"]
Header --> Badges["BlackboardBadges component"]
Header --> Toggle{"open?"}
Toggle --> |No| End(["Collapsed view"])
Toggle --> |Yes| Content["Render content with search and sections"]
Content --> Search["SearchBar component"]
Content --> Sections["Collapsible sections:<br/>- Plan<br/>- Step Results<br/>- Facts<br/>- Reflections<br/>- Final Output"]
Sections --> End(["Expanded view"])
```

**Diagram sources**
- [BlackboardPanel.tsx:10-52](file://frontend/src/components/chat/BlackboardPanel.tsx#L10-L52)
- [BlackboardPanel.tsx:54-67](file://frontend/src/components/chat/BlackboardPanel.tsx#L54-L67)
- [BlackboardPanel.tsx:69-82](file://frontend/src/components/chat/BlackboardPanel.tsx#L69-L82)
- [BlackboardPanel.tsx:84-177](file://frontend/src/components/chat/BlackboardPanel.tsx#L84-L177)
- [BlackboardPanel.tsx:179-196](file://frontend/src/components/chat/BlackboardPanel.tsx#L179-L196)

**Section sources**
- [BlackboardPanel.tsx:10-52](file://frontend/src/components/chat/BlackboardPanel.tsx#L10-L52)
- [BlackboardPanel.tsx:54-67](file://frontend/src/components/chat/BlackboardPanel.tsx#L54-L67)
- [BlackboardPanel.tsx:69-82](file://frontend/src/components/chat/BlackboardPanel.tsx#L69-L82)
- [BlackboardPanel.tsx:84-177](file://frontend/src/components/chat/BlackboardPanel.tsx#L84-L177)
- [BlackboardPanel.tsx:179-196](file://frontend/src/components/chat/BlackboardPanel.tsx#L179-L196)

### Specialized Blocks

#### ThoughtBlock Analysis
- Purpose: Collapsible reasoning display with intelligent preview and expand/collapse functionality.
- Implementation: Extends CollapsibleBlock with brain circuit icon and reasoning content.
- Features: Auto-truncation at 500 characters, show more/less toggle, and responsive design.

#### ReflectionBlock Analysis
- Purpose: Collapsible reflection summary with suggested action badges and detailed analysis.
- Implementation: Extends CollapsibleBlock with warning triangle icon and detailed information panel.
- Features: Action-specific badge colors, attempt tracking, and expandable details section.
- **Updated** Removed `space-y-0.5` CSS class from list items for improved layout spacing consistency.

#### ToolBlock Analysis
- Purpose: Collapsible tool invocation/results display with unified arguments/results presentation.
- Implementation: Extends CollapsibleBlock with wrench icon and uses ToolContentBlock for content display.
- Features: Status-specific icons, MCP source indication, and intelligent argument parsing.

#### PlanStepBlock Analysis
- Purpose: Collapsible execution step with status, duration, error handling, and nested child rendering.
- Implementation: Extends CollapsibleBlock with status-specific icons and duration display.
- Features: Auto-open when running, user override capability, retry indication, and nested ChatMessageRenderer.

#### MemoryBlock Analysis
- Purpose: Specialized memory read operation display using unified ToolContentBlock.
- Implementation: Uses CollapsibleBlock with book icon and accent color scheme.
- Features: Memory operation type labeling and ToolContentBlock integration.

```mermaid
classDiagram
class CollapsibleBlock {
+icon? : ReactNode
+label : ReactNode
+statusIcon? : ReactNode
+badge? : ReactNode
+defaultOpen? : boolean
+open? : boolean
+onOpenChange? : Function
+className? : string
+children : ReactNode
+headerExtra? : ReactNode
}
class ThoughtBlock {
+content : string
+reasoning? : string
}
class ReflectionBlock {
+summary : string
+suggestedAction : string
+rootCause : string
+failureAnalysis : string
+actionPlan : string
+reasoning : string
+hypotheses : string[]
+attempt : number
+maxAttempts : number
}
class ToolBlock {
+toolName : string
+args : string
+parsedArgs? : Record<string,unknown>
+result? : string
+resultLen? : number
+status : "running"|"success"|"error"|"awaiting_confirmation"
+source? : string
}
class PlanStepBlock {
+stepId : string
+stepNum : number
+title : string
+description? : string
+status : "running"|"completed"|"failed"
+duration? : number
+error? : string
+isRetry? : boolean
+children : DisplayItem[]
}
class MemoryBlock {
+toolName : string
+args : string
+parsedArgs? : Record<string,unknown>
+result? : string
+resultLen? : number
+status : "running"|"success"|"error"
}
class ToolContentBlock {
+args : string
+result? : string
+resultLen? : number
+borderClass? : string
}
class BlackboardPanel {
+open : boolean
+search : string
+bbState : BlackboardState
}
class BlackboardBadges {
+state : BlackboardState
}
class SearchBar {
+value : string
+onChange : Function
}
class BlackboardContent {
+state : BlackboardState
+search : string
}
class CollapsibleSection {
+title : string
+count : number
+children : ReactNode
}
class useMessageSender {
+send(messageText) : Promise<void>
+cancel() : Promise<void>
+isProcessing : boolean
}
ThoughtBlock --|> CollapsibleBlock
ReflectionBlock --|> CollapsibleBlock
ToolBlock --|> CollapsibleBlock
PlanStepBlock --|> CollapsibleBlock
MemoryBlock --|> CollapsibleBlock
ToolBlock --> ToolContentBlock
MemoryBlock --> ToolContentBlock
BlackboardPanel --> BlackboardBadges
BlackboardPanel --> SearchBar
BlackboardPanel --> BlackboardContent
BlackboardContent --> CollapsibleSection
useMessageSender --> ChatInput : "used by"
```

**Diagram sources**
- [CollapsibleBlock.tsx:10-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L10-L67)
- [ThoughtBlock.tsx:10-45](file://frontend/src/components/chat/ThoughtBlock.tsx#L10-L45)
- [ReflectionBlock.tsx:13-63](file://frontend/src/components/chat/ReflectionBlock.tsx#L13-L63)
- [ToolBlock.tsx:16-38](file://frontend/src/components/chat/ToolBlock.tsx#L16-L38)
- [PlanStepBlock.tsx:17-78](file://frontend/src/components/chat/PlanStepBlock.tsx#L17-L78)
- [MemoryBlock.tsx:52-74](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L52-L74)
- [ToolContentBlock.tsx:35-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L35-L78)
- [BlackboardPanel.tsx:10-52](file://frontend/src/components/chat/BlackboardPanel.tsx#L10-L52)
- [BlackboardPanel.tsx:54-67](file://frontend/src/components/chat/BlackboardPanel.tsx#L54-L67)
- [BlackboardPanel.tsx:69-82](file://frontend/src/components/chat/BlackboardPanel.tsx#L69-L82)
- [BlackboardPanel.tsx:84-177](file://frontend/src/components/chat/BlackboardPanel.tsx#L84-L177)
- [BlackboardPanel.tsx:179-196](file://frontend/src/components/chat/BlackboardPanel.tsx#L179-L196)
- [useMessageSender.ts:21-83](file://frontend/src/hooks/useMessageSender.ts#L21-L83)

**Section sources**
- [ThoughtBlock.tsx:1-45](file://frontend/src/components/chat/ThoughtBlock.tsx#L1-L45)
- [ReflectionBlock.tsx:1-63](file://frontend/src/components/chat/ReflectionBlock.tsx#L1-L63)
- [ToolBlock.tsx:1-38](file://frontend/src/components/chat/ToolBlock.tsx#L1-L38)
- [PlanStepBlock.tsx:1-78](file://frontend/src/components/chat/PlanStepBlock.tsx#L1-L78)
- [ChatMessageRenderer.tsx:52-74](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L52-L74)
- [ToolContentBlock.tsx:1-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L1-L78)
- [BlackboardPanel.tsx:1-196](file://frontend/src/components/chat/BlackboardPanel.tsx#L1-L196)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)

## Dependency Analysis
- Stores:
  - chatStore: Holds messages, streamingText, isThinking, isTaskActive, activityStatus, and provides actions to add/update messages, stream tokens, set activity, resolve actions, and clear session UI state. Also exposes selectors for pending actions and grouping logic.
  - planStore: Manages planGroups, session stats, and provides actions to set plan, update step status, add steps, and clear plan state. Now central to the new plan-based architecture.
  - blackboardStore: Manages real-time execution state with loading, error, and state properties. Provides stable selectors for state queries and debounced state updates.
  - sessionStore: Manages session lifecycle with session creation, deletion, listing, renaming, archiving, and activity tracking.
- Libraries:
  - markdownConfig: Provides customSchema and markdownComponents for ReactMarkdown rendering.
  - dagLayout: Computes DAG layout for visualization.
  - chatUtils: Advanced message conversion and grouping with plan reconstruction capabilities.
  - chatUtilsHelpers: Message grouping helpers including tool key generation and content reconstruction.
  - chatGroupingHandlers: Specialized handlers for plan steps, tool calls/results, and action messages.
- Components depend on stores for state and on libraries for rendering/formatting.
- **Updated** Event System:
  - useBlackboardEvents: Handles debounced blackboard state updates via session events with 300ms debounce timing.
  - useMessageSender: Centralized hook that encapsulates all message sending logic and integrates with stores and APIs.
  - blackboard API: Provides direct access to blackboard state retrieval through Wails RPC calls.
  - chat API: Provides sendMessage and cancelTask functions for message operations.
  - sessions API: Provides createSession and other session management functions.

```mermaid
graph LR
CS["chatStore"] --> CMR["ChatMessageRenderer"]
PS["planStore"] --> EP["ExecutionPanels"]
PS --> PV["PlanView"]
BS["blackboardStore"] --> BB["BlackboardPanel"]
BS --> UBE["useBlackboardEvents"]
SS["sessionStore"] --> UMS["useMessageSender"]
UMS --> CHAT["chat API"]
UMS --> SESS["sessions API"]
DG["DAGGraph"] --> DL["dagLayout"]
AM["AssistantMessage"] --> MC["markdownConfig"]
CA["ChatArea"] --> CU["chatUtils"]
CA --> CS
EP --> DG
CMR --> THB["ThoughtBlock"]
CMR --> RFB["ReflectionBlock"]
CMR --> TB["ToolBlock"]
CMR --> TGB["ThoughtGroupBlock"]
CMR --> PSB["PlanStepBlock"]
CMR --> CB["CollapsibleBlock"]
CMR --> TCB["ToolContentBlock"]
CU --> CUH["chatUtilsHelpers"]
CU --> CGH["chatGroupingHandlers"]
BB --> BA["blackboard API"]
```

**Diagram sources**
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [sessionStore.ts:1-76](file://frontend/src/stores/sessionStore.ts#L1-L76)
- [ChatMessageRenderer.tsx:111-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L111-L126)
- [DAGGraph.tsx:13-88](file://frontend/src/components/chat/DAGGraph.tsx#L13-L88)
- [dagLayout.ts:33-237](file://frontend/src/lib/dagLayout.ts#L33-L237)
- [markdownConfig.tsx:27-77](file://frontend/src/lib/markdownConfig.tsx#L27-L77)
- [chatUtils.ts:1-176](file://frontend/src/lib/chatUtils.ts#L1-L176)
- [chatUtilsHelpers.ts:1-186](file://frontend/src/lib/chatUtilsHelpers.ts#L1-L186)
- [chatGroupingHandlers.ts:1-158](file://frontend/src/lib/chatGroupingHandlers.ts#L1-L158)
- [useBlackboardEvents.ts:1-59](file://frontend/src/hooks/events/useBlackboardEvents.ts#L1-L59)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)
- [chat.ts:1-56](file://frontend/src/api/chat.ts#L1-L56)
- [sessions.ts:1-56](file://frontend/src/api/sessions.ts#L1-L56)
- [blackboard.ts:1-17](file://frontend/src/api/blackboard.ts#L1-L17)

**Section sources**
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:1-100](file://frontend/src/stores/planStore.ts#L1-L100)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [sessionStore.ts:1-76](file://frontend/src/stores/sessionStore.ts#L1-L76)
- [ChatMessageRenderer.tsx:1-126](file://frontend/src/components/chat/ChatMessageRenderer.tsx#L1-L126)
- [useBlackboardEvents.ts:1-59](file://frontend/src/hooks/events/useBlackboardEvents.ts#L1-L59)
- [useMessageSender.ts:1-84](file://frontend/src/hooks/useMessageSender.ts#L1-L84)
- [chat.ts:1-56](file://frontend/src/api/chat.ts#L1-L56)
- [sessions.ts:1-56](file://frontend/src/api/sessions.ts#L1-L56)
- [blackboard.ts:1-17](file://frontend/src/api/blackboard.ts#L1-L17)

## Performance Considerations
- Memoization and lazy rendering:
  - AssistantMessage memoizes highlighted raw Markdown to avoid repeated highlighting.
  - All collapsible blocks use React.memo for optimal re-render performance.
  - ChatMessageRenderer uses React.memo for MemoryBlock and ThoughtGroupBlock to minimize re-renders.
  - ChatArea measures container height efficiently using ResizeObserver and requestAnimationFrame fallbacks.
  - **Updated** BlackboardPanel uses useMemo for filtered content to optimize search performance.
- Streaming:
  - ChatInput sets isTaskActive and activityStatus during send; streamingText is appended incrementally via chatStore to avoid full re-renders of the entire message list.
  - **Updated** useMessageSender centralizes all state updates, reducing redundant re-renders across components.
- Collapsing and virtualization:
  - All collapsible components use CollapsibleBlock for consistent performance and reduced DOM complexity.
  - Pinned user message is rendered outside the scrollable region to keep the list lean.
  - **Updated** BlackboardPanel sections use local state for individual collapsible sections to minimize re-renders.
- Rendering optimization:
  - Markdown rendering uses plugins and sanitization once per component; Mermaid diagrams are wrapped in ErrorBoundary to prevent cascading failures.
  - ToolContentBlock uses efficient truncation algorithms and conditional rendering.
  - **Updated** BlackboardPanel implements debounced search with 300ms delay to prevent excessive re-computation.
- **Updated** Event System Performance:
  - Blackboard state updates are debounced with 300ms delay to handle rapid state changes efficiently.
  - useMessageSender reduces component re-renders by centralizing state management and providing stable callbacks.
  - Event cleanup ensures timers are properly cleared when components unmount.
- **Updated** Centralized Message Handling:
  - useMessageSender uses useCallback to provide stable function references, preventing unnecessary re-renders in components that depend on it.
  - Direct Zustand store access in useMessageSender eliminates intermediate state updates that could cause re-renders.

## Accessibility Features
- Interactive elements:
  - Buttons and toggles include aria-labels and titles for screen readers.
  - Hover states reveal opacity-based controls for discoverability.
  - **Updated** BlackboardPanel search input includes proper ARIA attributes and keyboard navigation.
  - **Updated** useMessageSender provides consistent accessibility patterns across all message operations.
- Focus management:
  - Pinned messages use tabIndex and onBlur handlers to manage expansion/collapse.
  - Collapsible components expose trigger buttons with appropriate ARIA attributes.
  - **Updated** BlackboardPanel sections maintain focus order and keyboard navigation.
  - **Updated** ChatInput maintains proper focus states when switching between send and cancel modes.
- Semantic structure:
  - CollapsibleBlock uses native HTML semantics with proper headings and lists.
  - All collapsible components provide consistent keyboard navigation and screen reader support.
  - ToolContentBlock provides accessible expand/collapse functionality.
  - **Updated** BlackboardPanel uses semantic HTML structure with proper sectioning and labeling.
  - **Updated** useMessageSender maintains accessibility by preserving component focus and keyboard navigation.

## Customization Options
- Foundation components:
  - CollapsibleBlock allows custom icons, labels, status indicators, badges, and styling through props.
  - ToolContentBlock supports custom border classes and integrates with various content types.
  - ScrollContext enables global navigation coordination across components.
- Markdown rendering:
  - customSchema extends rehype-sanitize to allow code spans/classes and heading IDs.
  - markdownComponents override code blocks, enabling language badges, Mermaid diagrams, and pre wrappers.
- Message types:
  - chatStore defines extensive MessageType and DisplayItem kinds to support diverse content types (thoughts, tools, reflections, plan steps, memory reads, etc.).
  - **Updated** Enhanced metadata typing provides better type safety for action resolutions.
- Execution visualization:
  - PlanView and DAGGraph adapt to backend-provided plan data; durations, statuses, and dependencies are rendered with appropriate icons and colors.
- Tool rendering:
  - ToolBlock supports source differentiation (e.g., MCP) and long argument/result previews with expand/collapse.
- **Updated** BlackboardPanel Customization:
  - Adaptive spacing based on sidebar and file viewer panel states.
  - Configurable search functionality with customizable placeholder text.
  - Badge indicators can be extended to show additional blackboard element types.
  - Collapsible sections can be customized for different content types.
- **Updated** useMessageSender Customization:
  - Centralized error handling can be extended with custom error types and recovery strategies.
  - Session creation logic can be customized for different authentication or authorization requirements.
  - Message sending flow can be adapted for different backend protocols or message formats.

**Section sources**
- [CollapsibleBlock.tsx:10-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L10-L67)
- [ToolContentBlock.tsx:27-78](file://frontend/src/components/chat/ToolContentBlock.tsx#L27-L78)
- [ScrollContext.tsx:1-37](file://frontend/src/components/chat/ScrollContext.tsx#L1-L37)
- [markdownConfig.tsx:7-77](file://frontend/src/lib/markdownConfig.tsx#L7-L77)
- [chatStore.ts:3-41](file://frontend/src/stores/chatStore.ts#L3-L41)
- [PlanView.tsx:1-65](file://frontend/src/components/chat/PlanView.tsx#L1-L65)
- [DAGGraph.tsx:13-88](file://frontend/src/components/chat/DAGGraph.tsx#L13-L88)
- [ToolBlock.tsx:16-38](file://frontend/src/components/chat/ToolBlock.tsx#L16-L38)
- [BlackboardPanel.tsx:24-52](file://frontend/src/components/chat/BlackboardPanel.tsx#L24-L52)
- [useMessageSender.ts:12-19](file://frontend/src/hooks/useMessageSender.ts#L12-L19)
- [messages.ts:55-125](file://frontend/src/types/messages.ts#L55-L125)

## Troubleshooting Guide
- Messages not appearing:
  - Verify activeSessionId exists; ChatArea shows empty state when no session is active.
  - Check GetSessionHistory call and chatUtils conversion; errors are logged and displayed as a banner.
  - **Updated** Check useMessageSender hook for proper session creation and message addition.
- Streaming text not visible:
  - Ensure chatStore.streamingText is being updated; ChatMessageRenderer renders streamingText as an AssistantMessage with isStreaming.
  - **Updated** Verify useMessageSender is properly setting streaming state and calling sendMessage.
- Plan not visible:
  - Confirm planStore.planGroups is populated; ExecutionPanels only renders when both planGroups and activeSessionId exist.
- Tool results missing:
  - Results may arrive before tool_call; chatUtils.groupMessages buffers results keyed by tool_call_id or composite keys and applies them when tool_call arrives.
- Pinned message not expanding:
  - Ensure maxHeight is set and ResizeObserver is available; pinned messages require naturalHeight measurement to decide overflow.
- Collapsible components not working:
  - Verify CollapsibleBlock is properly imported and used as the base component.
  - Check that controlled/uncontrolled mode props are correctly configured.
- Scroll navigation issues:
  - Ensure ScrollContext provider is wrapping the component tree.
  - Verify that scrollToStep callback is properly set and used.
- **Updated** useMessageSender Issues:
  - Hook not responding to message sending: Check that useMessageSender is properly imported and used in ChatInput.
  - Session creation failing: Verify createSession API call and error handling in useMessageSender.
  - State not updating: Ensure Zustand store updates are properly triggered and not being overridden by component state.
  - Error handling not working: Check logger integration and error message addition in useMessageSender.
- **Updated** BlackboardPanel Issues:
  - Blackboard state not updating: Check useBlackboardEvents hook for proper event subscription and debounce timing.
  - Search not working: Verify search state is properly managed and filtered content is computed correctly.
  - Badges not showing: Ensure blackboard state contains the expected data structure with non-empty arrays.
  - Panel not rendering: Confirm hasBB flag is true and activeSessionId exists before component mounts.
- **Updated** Metadata Type Issues:
  - Action resolution not typed: Check that metadata types are properly defined and used with helper functions.
  - Type errors in action components: Verify that resolution metadata follows the expected structure and type guards are applied correctly.

**Section sources**
- [ChatArea.tsx:91-140](file://frontend/src/components/chat/ChatArea.tsx#L91-L140)
- [chatStore.ts:468-570](file://frontend/src/stores/chatStore.ts#L468-L570)
- [planStore.ts:57-99](file://frontend/src/stores/planStore.ts#L57-L99)
- [chatUtils.ts:117-175](file://frontend/src/lib/chatUtils.ts#L117-L175)
- [CollapsibleBlock.tsx:23-67](file://frontend/src/components/chat/CollapsibleBlock.tsx#L23-L67)
- [ScrollContext.tsx:12-37](file://frontend/src/components/chat/ScrollContext.tsx#L12-L37)
- [useBlackboardEvents.ts:12-44](file://frontend/src/hooks/events/useBlackboardEvents.ts#L12-L44)
- [blackboardStore.ts:44-53](file://frontend/src/stores/blackboardStore.ts#L44-L53)
- [useMessageSender.ts:24-70](file://frontend/src/hooks/useMessageSender.ts#L24-L70)
- [messages.ts:85-125](file://frontend/src/types/messages.ts#L85-L125)

## Conclusion
C0WRK's chat interface has been completely rewritten with a modern, plan-based architecture that provides enhanced message grouping capabilities and improved component composition patterns. The new foundation includes CollapsibleBlock.tsx for consistent collapsible UI patterns, ScrollContext.tsx for coordinated navigation, and ToolContentBlock.tsx for unified tool display. The chatUtils.ts and chatUtilsHelpers.ts libraries now feature advanced message grouping with specialized handlers for plan steps, tools, and actions. The migration from panel-based to plan-based architecture provides better execution visualization and state management through planStore.ts. The rendering pipeline converts backend messages into a rich, interactive UI with collapsible blocks, streaming support, and robust error boundaries, while maintaining accessibility, performance, and extensibility for diverse execution contexts.

**Updated** Recent additions include the useMessageSender hook that provides centralized message handling, significantly improving code organization and maintainability. The hook encapsulates complex session creation, message sending, and cancellation handling, removing over 50 lines of duplicated code from ChatInput and centralizing error handling. Enhanced metadata typing in messages.ts provides structured types for action resolutions, improving type safety and developer experience. The BlackboardPanel component provides real-time visibility into agent task execution through a sophisticated event system with debounced updates, comprehensive search functionality, and badge indicators for different execution elements. The integration with blackboardStore and useBlackboardEvents ensures efficient state management and responsive user experience. The component seamlessly adapts to application layout changes and provides essential debugging and monitoring capabilities for complex agent workflows.