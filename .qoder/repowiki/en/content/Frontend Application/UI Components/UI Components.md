# UI Components

<cite>
**Referenced Files in This Document**
- [AppLayout.tsx](file://frontend/src/components/layout/AppLayout.tsx)
- [Sidebar.tsx](file://frontend/src/components/layout/Sidebar.tsx)
- [WorkspacePanel.tsx](file://frontend/src/components/layout/WorkspacePanel.tsx)
- [FileTreePanel.tsx](file://frontend/src/components/layout/FileTreePanel.tsx)
- [FileIcon.tsx](file://frontend/src/components/layout/FileIcon.tsx)
- [AssistantMessage.tsx](file://frontend/src/components/chat/AssistantMessage.tsx)
- [UserMessage.tsx](file://frontend/src/components/chat/UserMessage.tsx)
- [ChatInput.tsx](file://frontend/src/components/chat/ChatInput.tsx)
- [PlanView.tsx](file://frontend/src/components/chat/PlanView.tsx)
- [FileViewerPanel.tsx](file://frontend/src/components/fileViewer/FileViewerPanel.tsx)
- [FileViewerContent.tsx](file://frontend/src/components/fileViewer/FileViewerContent.tsx)
- [FileViewerTabBar.tsx](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx)
- [CodebaseMemoryBanner.tsx](file://frontend/src/components/CodebaseMemoryBanner.tsx)
- [RtkBanner.tsx](file://frontend/src/components/RtkBanner.tsx)
- [MCPServerCard.tsx](file://frontend/src/components/settings/MCPServerCard.tsx)
- [MCPServerForm.tsx](file://frontend/src/components/settings/MCPServerForm.tsx)
- [MCPSettings.tsx](file://frontend/src/components/settings/MCPSettings.tsx)
- [button.tsx](file://frontend/src/components/ui/button.tsx)
- [dialog.tsx](file://frontend/src/components/ui/dialog.tsx)
- [tooltip.tsx](file://frontend/src/components/ui/tooltip.tsx)
- [input.tsx](file://frontend/src/components/ui/input.tsx)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
</cite>

## Update Summary
**Changes Made**
- Updated File Viewer Components section to reflect modernized file navigation and improved scroll preservation
- Added new utility components section for CodebaseMemoryBanner and RtkBanner
- Completely rewrote MCP Settings section to document the new MCPServerCard, MCPServerForm, and MCPSettings components
- Updated AppLayout integration to include new banner components
- Enhanced file viewer component analysis with improved error handling and workspace event subscription

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
This document describes C0WRK's React component library and UI system. It focuses on:
- Chat interface components: AssistantMessage, UserMessage, ChatInput, PlanView
- Layout components: AppLayout, Sidebar, WorkspacePanel, FileTreePanel
- UI primitives from shadcn/ui: Button, Dialog, Tooltip, Input
- File viewer system: FileViewerPanel, FileViewerContent, FileViewerTabBar
- Utility components: CodebaseMemoryBanner, RtkBanner
- MCP settings system: MCPServerCard, MCPServerForm, MCPSettings
It explains component responsibilities, props, customization, styling patterns, accessibility, and composition patterns for building complex UI interactions.

## Project Structure
C0WRK organizes UI under frontend/src/components, grouped by domain:
- layout: AppLayout, Sidebar, WorkspacePanel, FileTreePanel, FileIcon
- chat: AssistantMessage, UserMessage, ChatInput, PlanView, and related helpers
- fileViewer: FileViewerPanel, FileViewerContent, FileViewerTabBar
- settings: MCPServerCard, MCPServerForm, MCPSettings, and other configuration components
- ui: shadcn/ui wrappers and variants for Button, Dialog, Tooltip, Input
- utilities: CodebaseMemoryBanner, RtkBanner for system notifications
- stores: chatStore, sessionStore, fileViewerStore, panelStore, etc.
- lib: markdownConfig, diffParser, formatters, utils

```mermaid
graph TB
subgraph "Layout"
AL["AppLayout"]
SB["Sidebar"]
WP["WorkspacePanel"]
FTP["FileTreePanel"]
FI["FileIcon"]
end
subgraph "Chat"
AM["AssistantMessage"]
UM["UserMessage"]
CI["ChatInput"]
PV["PlanView"]
end
subgraph "File Viewer"
FVP["FileViewerPanel"]
FVC["FileViewerContent"]
FVTB["FileViewerTabBar"]
end
subgraph "Utility Components"
CMB["CodebaseMemoryBanner"]
RTKB["RtkBanner"]
end
subgraph "MCP Settings"
MSC["MCPServerCard"]
MSF["MCPServerForm"]
MCP["MCPSettings"]
end
subgraph "UI Primitives"
BTN["Button"]
DLG["Dialog"]
TIP["Tooltip"]
INP["Input"]
end
AL --> SB
AL --> CI
AL --> PV
AL --> FVP
AL --> CMB
AL --> RTKB
SB --> WP
WP --> FTP
FTP --> FI
FVP --> FVTB
FVP --> FVC
MSC --> MCF
MCP --> MSC
MCP --> MSF
AM --> DLG
UM --> TIP
CI --> BTN
FVC --> INP
```

**Diagram sources**
- [AppLayout.tsx:1-91](file://frontend/src/components/layout/AppLayout.tsx#L1-L91)
- [Sidebar.tsx:1-627](file://frontend/src/components/layout/Sidebar.tsx#L1-L627)
- [WorkspacePanel.tsx:1-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L1-L71)
- [FileTreePanel.tsx:1-483](file://frontend/src/components/layout/FileTreePanel.tsx#L1-L483)
- [FileIcon.tsx](file://frontend/src/components/layout/FileIcon.tsx)
- [AssistantMessage.tsx:1-91](file://frontend/src/components/chat/AssistantMessage.tsx#L1-L91)
- [UserMessage.tsx:1-104](file://frontend/src/components/chat/UserMessage.tsx#L1-L104)
- [ChatInput.tsx:1-193](file://frontend/src/components/chat/ChatInput.tsx#L1-L193)
- [PlanView.tsx:1-153](file://frontend/src/components/chat/PlanView.tsx#L1-L153)
- [FileViewerPanel.tsx:1-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L1-L37)
- [FileViewerContent.tsx:1-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L1-L208)
- [FileViewerTabBar.tsx:1-157](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L1-L157)
- [CodebaseMemoryBanner.tsx:1-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L1-L54)
- [RtkBanner.tsx:1-54](file://frontend/src/components/RtkBanner.tsx#L1-L54)
- [MCPServerCard.tsx:1-79](file://frontend/src/components/settings/MCPServerCard.tsx#L1-L79)
- [MCPServerForm.tsx:1-172](file://frontend/src/components/settings/MCPServerForm.tsx#L1-L172)
- [MCPSettings.tsx:1-150](file://frontend/src/components/settings/MCPSettings.tsx#L1-L150)
- [button.tsx:1-32](file://frontend/src/components/ui/button.tsx#L1-L32)
- [dialog.tsx:1-159](file://frontend/src/components/ui/dialog.tsx#L1-L159)
- [tooltip.tsx:1-56](file://frontend/src/components/ui/tooltip.tsx#L1-L56)
- [input.tsx:1-22](file://frontend/src/components/ui/input.tsx#L1-L22)

**Section sources**
- [AppLayout.tsx:1-91](file://frontend/src/components/layout/AppLayout.tsx#L1-L91)
- [Sidebar.tsx:1-627](file://frontend/src/components/layout/Sidebar.tsx#L1-L627)
- [WorkspacePanel.tsx:1-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L1-L71)
- [FileTreePanel.tsx:1-483](file://frontend/src/components/layout/FileTreePanel.tsx#L1-L483)
- [FileViewerPanel.tsx:1-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L1-L37)
- [FileViewerContent.tsx:1-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L1-L208)
- [FileViewerTabBar.tsx:1-157](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L1-L157)
- [CodebaseMemoryBanner.tsx:1-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L1-L54)
- [RtkBanner.tsx:1-54](file://frontend/src/components/RtkBanner.tsx#L1-L54)
- [MCPServerCard.tsx:1-79](file://frontend/src/components/settings/MCPServerCard.tsx#L1-L79)
- [MCPServerForm.tsx:1-172](file://frontend/src/components/settings/MCPServerForm.tsx#L1-L172)
- [MCPSettings.tsx:1-150](file://frontend/src/components/settings/MCPSettings.tsx#L1-L150)
- [button.tsx:1-32](file://frontend/src/components/ui/button.tsx#L1-L32)
- [dialog.tsx:1-159](file://frontend/src/components/ui/dialog.tsx#L1-L159)
- [tooltip.tsx:1-56](file://frontend/src/components/ui/tooltip.tsx#L1-L56)
- [input.tsx:1-22](file://frontend/src/components/ui/input.tsx#L1-L22)

## Core Components
- AssistantMessage: Renders assistant content with optional raw Markdown view and streaming cursor.
- UserMessage: Renders user content with optional pinning, overflow handling, and time display.
- ChatInput: Text input with auto-resize, optimistic UI updates, session lifecycle, and cancellation.
- PlanView: Renders a plan as a list of steps with status badges, durations, and tooltips.
- AppLayout: Orchestrates sidebar, main chat area, pending actions, execution panels, file viewer, and status bar.
- Sidebar: Project/session management, settings, and workspace panel.
- WorkspacePanel: Tabs for Explorer, Git, Semantics; currently routes to FileTreePanel.
- FileTreePanel: Hierarchical file tree with filtering (glob/regex), lazy loading, and Git status coloring.
- FileViewerPanel/Content/TabBar: Multi-tab file viewer with syntax highlighting, Markdown rendering, diffs, and scroll preservation.
- CodebaseMemoryBanner/RtkBanner: System notification banners for MCP and RTK installation prompts.
- MCPServerCard/Form/Settings: Complete MCP server management system with collapsible cards and enhanced forms.
- UI primitives: Button, Dialog, Tooltip, Input wrappers around shadcn/ui and Radix primitives.

**Section sources**
- [AssistantMessage.tsx:20-91](file://frontend/src/components/chat/AssistantMessage.tsx#L20-L91)
- [UserMessage.tsx:3-104](file://frontend/src/components/chat/UserMessage.tsx#L3-L104)
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [PlanView.tsx:16-153](file://frontend/src/components/chat/PlanView.tsx#L16-L153)
- [AppLayout.tsx:30-91](file://frontend/src/components/layout/AppLayout.tsx#L30-L91)
- [Sidebar.tsx:64-627](file://frontend/src/components/layout/Sidebar.tsx#L64-L627)
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)
- [FileViewerPanel.tsx:9-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L9-L37)
- [FileViewerContent.tsx:18-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L18-L208)
- [FileViewerTabBar.tsx:18-157](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L18-L157)
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)
- [dialog.tsx:10-159](file://frontend/src/components/ui/dialog.tsx#L10-L159)
- [tooltip.tsx:19-56](file://frontend/src/components/ui/tooltip.tsx#L19-L56)
- [input.tsx:5-22](file://frontend/src/components/ui/input.tsx#L5-L22)

## Architecture Overview
The UI is composed of:
- Layout container (AppLayout) coordinating sidebar, chat, and file viewer panels
- Domain-specific panels (Sidebar, WorkspacePanel, FileTreePanel)
- Chat subsystem (AssistantMessage, UserMessage, ChatInput, PlanView)
- File viewer subsystem (FileViewerPanel, FileViewerContent, FileViewerTabBar)
- Utility components (CodebaseMemoryBanner, RtkBanner) for system notifications
- MCP settings subsystem (MCPServerCard, MCPServerForm, MCPSettings) for server management
- UI primitives (Button, Dialog, Tooltip, Input) wrapping shadcn/ui and Radix

```mermaid
graph TB
AL["AppLayout"] --> SB["Sidebar"]
AL --> CHAT["ChatArea<br/>ChatInput"]
AL --> PV["PlanView"]
AL --> FVP["FileViewerPanel"]
AL --> CMB["CodebaseMemoryBanner"]
AL --> RTKB["RtkBanner"]
SB --> WP["WorkspacePanel"]
WP --> FTP["FileTreePanel"]
FVP --> FVTB["FileViewerTabBar"]
FVP --> FVC["FileViewerContent"]
CMB --> MCF["MCP API"]
RTKB --> MCF
MSC["MCPServerCard"] --> MCF
MSF["MCPServerForm"] --> MCF
MCP["MCPSettings"] --> MSC
MCP --> MSF
AM["AssistantMessage"] --> DLG["Dialog"]
UM["UserMessage"] --> TIP["Tooltip"]
CI["ChatInput"] --> BTN["Button"]
FVC --> INP["Input"]
```

**Diagram sources**
- [AppLayout.tsx:30-91](file://frontend/src/components/layout/AppLayout.tsx#L30-L91)
- [Sidebar.tsx:64-627](file://frontend/src/components/layout/Sidebar.tsx#L64-L627)
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)
- [FileViewerPanel.tsx:9-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L9-L37)
- [FileViewerContent.tsx:18-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L18-L208)
- [FileViewerTabBar.tsx:18-157](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L18-L157)
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)
- [AssistantMessage.tsx:25-91](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L91)
- [UserMessage.tsx:10-104](file://frontend/src/components/chat/UserMessage.tsx#L10-L104)
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [PlanView.tsx:117-153](file://frontend/src/components/chat/PlanView.tsx#L117-L153)
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)
- [dialog.tsx:10-159](file://frontend/src/components/ui/dialog.tsx#L10-L159)
- [tooltip.tsx:19-56](file://frontend/src/components/ui/tooltip.tsx#L19-L56)
- [input.tsx:5-22](file://frontend/src/components/ui/input.tsx#L5-L22)

## Detailed Component Analysis

### Chat Components

#### AssistantMessage
- Purpose: Render assistant messages with Markdown support, optional raw source view, and streaming indicator.
- Props:
  - content: string
  - isStreaming?: boolean
- Behavior:
  - Uses remark/rehype plugins for GFM, emoji, breaks, slug, autolink headings, sanitization, external links.
  - Provides a toggle to switch between rendered Markdown and raw highlighted Markdown.
  - Streaming cursor rendered when isStreaming is true.
- Accessibility:
  - Hover-triggered button with title and aria-label for toggling raw view.
- Styling:
  - Conditional opacity and hover effects for the raw-view toggle.
  - Prose styling for rendered Markdown with dark mode invert.
- Complexity:
  - Memoized highlight calculation for raw mode.
- Customization:
  - Uses customSchema and markdownComponents from markdownConfig.

**Section sources**
- [AssistantMessage.tsx:20-91](file://frontend/src/components/chat/AssistantMessage.tsx#L20-L91)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)

#### UserMessage
- Purpose: Render user messages with optional pinning, overflow handling, and time display.
- Props:
  - content: string
  - timestamp: number
  - isPinned?: boolean
  - maxHeight?: number
- Behavior:
  - Measures natural height via ResizeObserver when pinned.
  - Collapsible behavior when overflowing; gradient overlay indicates truncation.
  - Focus management to collapse on blur.
- Accessibility:
  - Role="button" and aria-expanded when overflowed.
  - Tab index set for keyboard navigation.
- Styling:
  - Rounded corner styling for message bubble.
  - Gradient fade overlay when clipped.
- Customization:
  - Uses local state for expanded/collapsed and natural height measurement.

**Section sources**
- [UserMessage.tsx:3-104](file://frontend/src/components/chat/UserMessage.tsx#L3-L104)

#### ChatInput
- Purpose: Text input for sending messages with auto-resize, optimistic UI, session lifecycle, and cancellation.
- Props: none
- Behavior:
  - Auto-resizes textarea up to a capped height.
  - Optimistically adds user message to UI before backend confirmation.
  - Creates a session if none exists; integrates with Wails API.
  - Disables input when no project or task is active.
  - Supports cancellation via CancelTask.
  - Keyboard shortcut: Enter sends, Shift+Enter inserts newline.
- Stores and APIs:
  - Reads active session/project from stores; writes optimistic state.
  - Uses sessionStore, chatStore, and Wails API.
- Accessibility:
  - Properly disabled states and aria-labels on action buttons.
- Styling:
  - Custom scrollbar utility class applied to textarea.
- Customization:
  - Placeholder text adapts based on state (no project, processing, etc.).

```mermaid
sequenceDiagram
participant U as "User"
participant CI as "ChatInput"
participant SS as "sessionStore"
participant CS as "chatStore"
participant API as "Wails API"
U->>CI : "Enter text"
U->>CI : "Press Enter"
CI->>SS : "Read activeSessionId"
alt "No session"
CI->>API : "CreateSession()"
API-->>CI : "Session"
CI->>SS : "addSession(), setActiveSession()"
end
CI->>CS : "addMessage(optimitic)"
CI->>API : "SendMessage(sessionId, text)"
API-->>CI : "Stream/ack"
CI->>CS : "setTaskActive(false)"
```

**Diagram sources**
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)

**Section sources**
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)

#### PlanView
- Purpose: Visualize plan steps with status, optional details, and duration.
- Props: none (reads from panelStore)
- Behavior:
  - Maps plan groups to view steps, deriving labels and status.
  - Renders each step as a card with status icon/badge, optional details toggle, and duration display.
  - Uses StepTooltip for step descriptions.
- Stores:
  - Reads latest plan group from panelStore.
- Accessibility:
  - Interactive buttons with hover/active states; disabled buttons when no details.
- Styling:
  - Status-specific badges and icons; spacing and typography tuned for readability.

**Section sources**
- [PlanView.tsx:16-153](file://frontend/src/components/chat/PlanView.tsx#L16-L153)

### Layout Components

#### AppLayout
- Purpose: Top-level layout orchestrating sidebar, chat, execution panels, file viewer, and status bar.
- Features:
  - Resizable sidebar and file viewer panels with persisted widths.
  - Collapsible sidebar and file viewer with expand triggers.
  - Empty state when no projects are present.
  - Integrates ChatInput, PlanView, and FileViewerPanel.
  - Hosts utility banners for system notifications.
- Stores:
  - Uses UI store for sidebar collapse state and file viewer store for widths and collapse state.
- Accessibility:
  - Buttons with titles and aria-labels for collapsing/expanding panels.

**Updated** Now includes CodebaseMemoryBanner and RtkBanner integration at the top of the layout.

**Section sources**
- [AppLayout.tsx:30-91](file://frontend/src/components/layout/AppLayout.tsx#L30-L91)

#### Sidebar
- Purpose: Project and session management, settings, and workspace panel.
- Features:
  - Project list with rename/delete actions.
  - Session list with rename/archive/delete; search/filter.
  - Settings modal and create project dialog.
  - WorkspacePanel integration.
- Events:
  - Subscribes to backend events for projects/sessions and switches project automatically when ready.
- Accessibility:
  - Dropdown menus with keyboard navigation; proper aria labels on triggers.

**Section sources**
- [Sidebar.tsx:64-627](file://frontend/src/components/layout/Sidebar.tsx#L64-L627)

#### WorkspacePanel
- Purpose: Tabbed workspace area with Explorer, Git, Semantics.
- Behavior:
  - Uses Tabs/TabsList/TabsTrigger with TooltipProvider.
  - Currently renders FileTreePanel in Explorer tab.
- Accessibility:
  - Tooltip labels for tab triggers; aria-labels on triggers.

**Section sources**
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)

#### FileTreePanel
- Purpose: Hierarchical file tree with filtering, lazy loading, and Git status indicators.
- Features:
  - Filter modes: glob and regex; debounced filtering.
  - Recursive filtering with forced expansion of matched ancestors.
  - Lazy loading of directory entries; loading spinners for directories.
  - Git status coloring per file/dir; directory inherits highest-priority color.
  - Double-click to open files; click to toggle directories.
- Stores:
  - Uses fileTreeStore for entries, expanded dirs, loading states, and Git status.
- Accessibility:
  - Tree semantics with aria-expanded on directories; keyboard-friendly interactions.

```mermaid
flowchart TD
Start(["Filter change"]) --> Debounce["Debounce input (300ms)"]
Debounce --> BuildMatcher["Build matcher (glob/regex)"]
BuildMatcher --> IsActive{"Filter active?"}
IsActive --> |No| ClearRecursive["Clear recursive entries"]
ClearRecursive --> RenderLazy["Render lazy entries"]
IsActive --> |Yes| EnsureRecursive["Ensure recursive entries loaded"]
EnsureRecursive --> Compute["Compute matched/visible/expanded sets"]
Compute --> RenderFiltered["Render filtered tree with forced expansions"]
```

**Diagram sources**
- [FileTreePanel.tsx:11-483](file://frontend/src/components/layout/FileTreePanel.tsx#L11-L483)

**Section sources**
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)

### File Viewer Components

#### FileViewerPanel
- Purpose: Container for file viewer tabs and content with resizable width.
- Props:
  - width: number
- Behavior:
  - Renders FileViewerTabBar and FileViewerContent.
  - Hidden when no files are open or panel is collapsed.
  - Implements collapsed state with narrow expand button.
- Accessibility:
  - Proper aria-labels for expand/collapse controls.

**Updated** Enhanced with improved collapsed state handling and better integration with AppLayout.

**Section sources**
- [FileViewerPanel.tsx:7-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L37)

#### FileViewerTabBar
- Purpose: Multi-tab UI for open files with close actions, dropdown menu, and collapse control.
- Behavior:
  - Scrolls active tab into view; shows tooltip with full path.
  - Dropdown lists all open files; marks active file.
  - Collapse button toggles file viewer collapse.
  - Uses FileIcon component for file type visualization.
  - Implements smooth scrolling to active tabs.
- Accessibility:
  - Proper role attributes and keyboard navigation.
  - TooltipProvider integration for file path tooltips.

**Updated** Improved tab scrolling with scrollToTab function and enhanced file icon integration.

**Section sources**
- [FileViewerTabBar.tsx:31-157](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L31-L157)

#### FileViewerContent
- Purpose: Displays file content with syntax highlighting, Markdown rendering, diffs, and scroll preservation.
- Behavior:
  - Handles loading/error/binary states with improved error handling.
  - Markdown preview vs raw source toggle.
  - Parses unified diffs and renders inline char diffs for modified lines.
  - Preserves scroll position across content updates using requestAnimationFrame.
  - Auto-refreshes on workspace:tree_changed events with silent reload.
  - Implements workspace event subscription for real-time updates.
- Stores:
  - Reads from fileViewerStore for active file and open files.
- Accessibility:
  - Error fallback and sanitized Markdown rendering.
  - Proper loading states and error messaging.

**Updated** Modernized with improved workspace event handling, better scroll preservation, and enhanced error states.

```mermaid
sequenceDiagram
participant WS as "Workspace Event"
participant FVC as "FileViewerContent"
participant API as "File API"
WS->>FVC : "workspace : tree_changed"
FVC->>FVC : "Save scroll position"
FVC->>API : "Silent reload all open files"
API-->>FVC : "Updated content"
FVC->>FVC : "Restore scroll position"
```

**Diagram sources**
- [FileViewerContent.tsx:47-59](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L47-L59)

**Section sources**
- [FileViewerContent.tsx:12-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L12-L208)

### Utility Components

#### CodebaseMemoryBanner
- Purpose: Prompts users to install Codebase Memory MCP for enhanced context awareness.
- Behavior:
  - Checks installation status on mount using sessionStorage for dismissal tracking.
  - Shows installation prompt with download button and dismiss option.
  - Handles installation progress and error states.
  - Uses sessionStorage to persist dismissal preferences.
- API Integration:
  - Integrates with checkCodebaseMemoryMCP and installCodebaseMemoryMCP APIs.
- Accessibility:
  - Proper button states and aria-labels for dismiss controls.

**New Component** Added as part of the modernization effort.

**Section sources**
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)

#### RtkBanner
- Purpose: Prompts users to install Runtime Toolkit for enhanced tool capabilities.
- Behavior:
  - Similar pattern to CodebaseMemoryBanner with installation checking and prompting.
  - Handles RTK installation progress and error states.
  - Uses sessionStorage for dismissal tracking.
- API Integration:
  - Integrates with checkRtk and installRtk APIs.
- Accessibility:
  - Consistent with CodebaseMemoryBanner patterns.

**New Component** Added as part of the modernization effort.

**Section sources**
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)

### MCP Settings Components

#### MCPServerCard
- Purpose: Displays MCP server configuration with collapsible details and status indicators.
- Props:
  - server: MCPServerStatus
  - tools: ToolInfo[]
  - expanded: boolean
  - onToggleExpand: () => void
  - onEdit: () => void
  - onDelete: () => void
- Behavior:
  - Collapsible card with chevron indicators for expansion state.
  - Shows connection status with visual indicators (CheckCircle2 for connected, AlertCircle for disconnected).
  - Displays transport type and tool count badges.
  - Shows server error messages when present.
  - Lists discovered tools with badge-style display.
  - Provides edit and delete action buttons.
- UI Components:
  - Uses Collapsible, CollapsibleContent, CollapsibleTrigger from shadcn/ui.
  - Integrates with Badge and Button components.
- Accessibility:
  - Proper role attributes for collapsible sections.
  - Clear visual indicators for server status.

**New Component** Part of the completely rewritten MCP settings system.

**Section sources**
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)

#### MCPServerForm
- Purpose: Enhanced form for adding and editing MCP server configurations.
- Props:
  - open: boolean
  - onOpenChange: (open: boolean) => void
  - editingName: string | null
  - serverConfigs: Record<string, MCPServerConfig>
  - editServer?: { name: string; transport: string }
  - isSaving: boolean
  - onSave: (config: Record<string, MCPServerConfig>, editName: string | null) => Promise<string | null>
- Behavior:
  - Supports both stdio and http transport types with conditional field rendering.
  - Dynamic environment variables and headers management with key-value entry lists.
  - Form validation with comprehensive error handling.
  - Edit mode support with pre-populated form data.
  - Transport type switching with automatic field population.
- Form Features:
  - Transport type selection with visual toggle buttons.
  - Command and arguments fields for stdio transport.
  - URL and headers fields for http transport.
  - Key-value pair management with add/remove functionality.
  - Real-time form validation with error display.
- API Integration:
  - Converts form data to MCPServerConfig format.
  - Handles save operations with error propagation.
- Accessibility:
  - Proper form labeling and error messaging.
  - Keyboard navigation support for dynamic field lists.

**New Component** Part of the completely rewritten MCP settings system.

**Section sources**
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)

#### MCPSettings
- Purpose: Main MCP settings page managing server configurations and system integrations.
- Features:
  - Dual installer cards for Codebase Memory and RTK with progress tracking.
  - Server management with add/edit/delete operations.
  - Real-time status monitoring and refresh capabilities.
  - Comprehensive error handling and user feedback.
- Components:
  - Integrates CodebaseMemoryBanner and RtkBanner for system notifications.
  - Uses MCPServerCard for server display and MCPServerForm for editing.
  - Implements InstallerCard component for MCP system management.
- API Integration:
  - Manages MCP status, tool lists, and server configurations.
  - Handles installation progress events and status updates.
  - Supports server configuration updates and deletions.
- State Management:
  - Tracks loading states, saving states, and error conditions.
  - Manages expanded server cards and form modal states.
- Accessibility:
  - Proper loading states and error messaging.
  - Clear action buttons with appropriate disabled states.

**New Component** Completely rewritten MCP settings system replacing the previous implementation.

**Section sources**
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)

### UI Primitive Components (shadcn/ui)

#### Button
- Purpose: Unified button primitive with variants and sizes.
- Props:
  - variant, size, asChild, className, and native button props.
- Variants:
  - Uses button-variants for consistent styling across the app.

**Section sources**
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)

#### Dialog
- Purpose: Modal dialog with overlay, portal, and optional close button.
- Slots:
  - Root, Trigger, Portal, Overlay, Content, Header/Footer, Title, Description, Close.
- Behavior:
  - Overlay animation and focus management; optional close button.

**Section sources**
- [dialog.tsx:10-159](file://frontend/src/components/ui/dialog.tsx#L10-L159)

#### Tooltip
- Purpose: Tooltip provider, trigger, and content with portal and arrow.
- Props:
  - delayDuration, sideOffset, and native TooltipPrimitive props.
- Behavior:
  - Animated positioning with arrow; supports bottom/left/right/top placement.

**Section sources**
- [tooltip.tsx:19-56](file://frontend/src/components/ui/tooltip.tsx#L19-L56)

#### Input
- Purpose: Styled input field with validation state support.
- Props:
  - type, className, and native input props.
- Accessibility:
  - aria-invalid classes for invalid states.

**Section sources**
- [input.tsx:5-22](file://frontend/src/components/ui/input.tsx#L5-L22)

## Dependency Analysis
- Component coupling:
  - AppLayout depends on Sidebar, FileViewerPanel, and chat components.
  - Sidebar depends on project/session stores and uses WorkspacePanel.
  - FileTreePanel depends on fileTreeStore and emits workspace events.
  - FileViewer components depend on fileViewerStore.
  - Chat components depend on chatStore and sessionStore.
  - Utility components (CodebaseMemoryBanner, RtkBanner) depend on MCP API.
  - MCP settings components depend on MCP API and form state management.
- External libraries:
  - Markdown rendering via remark/rehype ecosystem.
  - Syntax highlighting via highlight.js.
  - Pattern matching via picomatch.
  - Icons via lucide-react.
  - State via stores (Zustand-like stores).
  - Collapsible components via shadcn/ui.
- Potential circular dependencies:
  - None apparent among these components; stores are leaf dependencies.

```mermaid
graph LR
AL["AppLayout"] --> SB["Sidebar"]
AL --> FVP["FileViewerPanel"]
AL --> CMB["CodebaseMemoryBanner"]
AL --> RTKB["RtkBanner"]
AL --> CHAT["ChatInput/PlanView"]
SB --> WP["WorkspacePanel"]
WP --> FTP["FileTreePanel"]
FVP --> FVTB["FileViewerTabBar"]
FVP --> FVC["FileViewerContent"]
FTP --> FI["FileIcon"]
AM["AssistantMessage"] --> MC["markdownConfig"]
CI["ChatInput"] --> SS["sessionStore"]
CI --> CS["chatStore"]
FVC --> HL["highlight.js"]
FTP --> PM["picomatch"]
CMB --> MCF["MCP API"]
RTKB --> MCF
MSC --> MCF
MSF --> MCF
MCP --> MSC
MCP --> MSF
```

**Diagram sources**
- [AppLayout.tsx:30-91](file://frontend/src/components/layout/AppLayout.tsx#L30-L91)
- [Sidebar.tsx:64-627](file://frontend/src/components/layout/Sidebar.tsx#L64-L627)
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)
- [FileViewerPanel.tsx:7-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L37)
- [FileViewerContent.tsx:12-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L12-L208)
- [AssistantMessage.tsx:25-91](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L91)
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)

**Section sources**
- [AppLayout.tsx:30-91](file://frontend/src/components/layout/AppLayout.tsx#L30-L91)
- [Sidebar.tsx:64-627](file://frontend/src/components/layout/Sidebar.tsx#L64-L627)
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)
- [FileViewerPanel.tsx:7-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L37)
- [FileViewerContent.tsx:12-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L12-L208)
- [AssistantMessage.tsx:25-91](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L91)
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)
- [sessionStore.ts](file://frontend/src/stores/sessionStore.ts)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)

## Performance Considerations
- Memoization:
  - AssistantMessage memoizes highlighted Markdown for raw mode.
  - FileViewerContent memoizes highlighted HTML and display lines.
  - FileTreePanel computes visibility and matched paths with memoization.
- Debouncing:
  - FileTreePanel debounces filter input to reduce re-computation.
- Lazy loading:
  - FileTreePanel lazily loads directory entries and conditionally loads recursive entries only when filtering is active.
- Rendering:
  - FileTreePanel avoids rendering hidden nodes when filters are active.
  - FileViewerContent preserves scroll position to avoid jank during updates using requestAnimationFrame.
  - MCP settings components use efficient state management with minimal re-renders.
- Accessibility:
  - Components use proper roles and aria attributes to minimize assistive tech overhead.

## Troubleshooting Guide
- ChatInput does not enable:
  - Ensure a project is selected; input is disabled until a project exists.
  - Verify sessionStore has an activeSessionId or that creation succeeds.
- AssistantMessage fails to render:
  - Check markdownConfig customSchema and markdownComponents.
  - Confirm remark/rehype plugin chain is intact.
- FileTreePanel shows "No matches found":
  - Switch filter mode (glob/regex) or adjust pattern.
  - Ensure recursive entries are loaded when filter is active.
- FileViewerContent shows unsupported format:
  - Binary files are intentionally not supported; open a text-based file.
- FileViewerContent scroll issues:
  - Check that workspace:tree_changed events are properly handled.
  - Verify scroll preservation logic is working correctly.
- MCP settings not loading:
  - Check MCP API connectivity and server configurations.
  - Verify installer progress events are received correctly.
- Utility banners not appearing:
  - Ensure CodebaseMemoryBanner and RtkBanner are included in AppLayout.
  - Check MCP API status and installation states.

**Section sources**
- [ChatInput.tsx:13-193](file://frontend/src/components/chat/ChatInput.tsx#L13-L193)
- [AssistantMessage.tsx:25-91](file://frontend/src/components/chat/AssistantMessage.tsx#L25-L91)
- [FileTreePanel.tsx:270-483](file://frontend/src/components/layout/FileTreePanel.tsx#L270-L483)
- [FileViewerContent.tsx:12-208](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L12-L208)
- [CodebaseMemoryBanner.tsx:8-54](file://frontend/src/components/CodebaseMemoryBanner.tsx#L8-L54)
- [RtkBanner.tsx:8-54](file://frontend/src/components/RtkBanner.tsx#L8-L54)
- [MCPServerCard.tsx:16-79](file://frontend/src/components/settings/MCPServerCard.tsx#L16-L79)
- [MCPServerForm.tsx:48-172](file://frontend/src/components/settings/MCPServerForm.tsx#L48-L172)
- [MCPSettings.tsx:13-150](file://frontend/src/components/settings/MCPSettings.tsx#L13-L150)
- [tooltip.tsx:19-56](file://frontend/src/components/ui/tooltip.tsx#L19-L56)
- [dialog.tsx:10-159](file://frontend/src/components/ui/dialog.tsx#L10-L159)

## Conclusion
C0WRK's UI system combines a robust layout with specialized chat and file viewer components, backed by shadcn/ui primitives. The chat components emphasize Markdown rendering, streaming UX, and plan visualization. The file tree and viewer provide efficient filtering, syntax highlighting, and diff visualization with improved scroll preservation and workspace event handling. The new utility components (CodebaseMemoryBanner, RtkBanner) provide seamless system integration and installation prompts. The completely rewritten MCP settings system offers a modern, user-friendly interface for managing server configurations with enhanced form validation and real-time status monitoring. The layout is highly configurable with resizable and collapsible panels. Together, these components enable a flexible, accessible, and performant developer experience with modernized file navigation and comprehensive system management capabilities.