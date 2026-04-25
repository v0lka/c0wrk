# Frontend Application

<cite>
**Referenced Files in This Document**
- [main.tsx](file://frontend/src/main.tsx)
- [App.tsx](file://frontend/src/App.tsx)
- [package.json](file://frontend/package.json)
- [vite.config.ts](file://frontend/vite.config.ts)
- [tsconfig.app.json](file://frontend/tsconfig.app.json)
- [index.css](file://frontend/src/index.css)
- [AppLayout.tsx](file://frontend/src/components/layout/AppLayout.tsx)
- [chatStore.ts](file://frontend/src/stores/chatStore.ts)
- [useWails.ts](file://frontend/src/hooks/useWails.ts)
- [wails.ts](file://frontend/src/lib/wails.ts)
- [api.ts](file://frontend/src/constants/api.ts)
- [SettingsModal.tsx](file://frontend/src/components/settings/SettingsModal.tsx)
- [settingsStore.ts](file://frontend/src/stores/settingsStore.ts)
- [button.tsx](file://frontend/src/components/ui/button.tsx)
- [useXTermTheme.ts](file://frontend/src/hooks/useXTermTheme.ts)
- [Terminal.tsx](file://frontend/src/components/terminal/Terminal.tsx)
- [blackboardStore.ts](file://frontend/src/stores/blackboardStore.ts)
- [useBlackboardEvents.ts](file://frontend/src/hooks/events/useBlackboardEvents.ts)
- [blackboard.ts](file://frontend/src/api/blackboard.ts)
- [button-variants.ts](file://frontend/src/components/ui/button-variants.ts)
- [useFileIcon.ts](file://frontend/src/hooks/useFileIcon.ts)
</cite>

## Update Summary
**Changes Made**
- Added comprehensive terminal theming system documentation with XTerm integration
- Enhanced blackboard state management documentation with debounced event handling
- Updated UI component library documentation with icon consistency improvements
- Expanded terminal theming implementation details and CSS custom property mapping
- Documented improved blackboard state management with loading/error states

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
10. [Appendices](#appendices)

## Introduction
This document describes the React 19 frontend application for C0WRK, focusing on component architecture, state management with custom stores, UI component library, and integration with the Wails backend. It covers the chat interface, file viewer system, workspace panel, settings modal, terminal theming system, and enhanced blackboard state management. It also explains the build system using Vite, TypeScript configuration, and styling with Tailwind CSS, along with responsive design patterns, accessibility considerations, and cross-platform UI concerns.

## Project Structure
The frontend is organized around a clear separation of concerns:
- Entry point initializes the React root, error boundary, and global styles.
- App wires up Wails runtime events and renders the main layout.
- Layout composes the sidebar, chat area, execution panels, input, file viewer, and status bar.
- Stores encapsulate state for chat, UI, sessions, projects, file tree, file viewer, settings, and blackboard state.
- Hooks abstract Wails bindings and runtime event handling, including terminal theming and blackboard state management.
- UI components are built with a small, theme-consistent library using Tailwind and Radix primitives.

```mermaid
graph TB
A["main.tsx<br/>React root"] --> B["App.tsx<br/>Error boundary + layout host"]
B --> C["AppLayout.tsx<br/>Main layout"]
C --> D["Sidebar<br/>(workspace panel)"]
C --> E["ChatArea<br/>(messages)"]
C --> F["ChatInput<br/>(input)"]
C --> G["ExecutionPanels<br/>(execution views)"]
C --> H["PendingActionsBar<br/>(pending actions)"]
C --> I["FileViewerPanel<br/>(file viewer)"]
C --> J["StatusBar<br/>(status bar)"]
C --> K["TerminalPanel<br/>(terminal with theming)"]
B --> L["useWails.ts<br/>Wails bindings + runtime"]
L --> M["useXTermTheme.ts<br/>XTerm theme system"]
M --> N["Terminal.tsx<br/>Terminal component"]
B --> O["chatStore.ts<br/>chat state + grouping"]
B --> P["blackboardStore.ts<br/>blackboard state + events"]
B --> Q["settingsStore.ts<br/>settings modal state"]
C --> R["ui/button.tsx<br/>UI primitive"]
```

**Diagram sources**
- [main.tsx:1-17](file://frontend/src/main.tsx#L1-L17)
- [App.tsx:1-91](file://frontend/src/App.tsx#L1-L91)
- [AppLayout.tsx:1-135](file://frontend/src/components/layout/AppLayout.tsx#L1-L135)
- [useWails.ts:1-61](file://frontend/src/hooks/useWails.ts#L1-L61)
- [useXTermTheme.ts:1-89](file://frontend/src/hooks/useXTermTheme.ts#L1-L89)
- [Terminal.tsx:1-134](file://frontend/src/components/terminal/Terminal.tsx#L1-L134)
- [chatStore.ts:1-571](file://frontend/src/stores/chatStore.ts#L1-L571)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [settingsStore.ts:1-20](file://frontend/src/stores/settingsStore.ts#L1-L20)
- [button.tsx:1-32](file://frontend/src/components/ui/button.tsx#L1-L32)

**Section sources**
- [main.tsx:1-17](file://frontend/src/main.tsx#L1-L17)
- [App.tsx:1-91](file://frontend/src/App.tsx#L1-L91)
- [AppLayout.tsx:1-135](file://frontend/src/components/layout/AppLayout.tsx#L1-L135)
- [package.json:1-61](file://frontend/package.json#L1-L61)
- [vite.config.ts:1-15](file://frontend/vite.config.ts#L1-L15)
- [tsconfig.app.json:1-5](file://frontend/tsconfig.app.json#L1-L5)
- [index.css:1-416](file://frontend/src/index.css#L1-L416)

## Core Components
- App: Initializes Wails runtime, listens for startup errors and vector index status events, and renders the layout with banners.
- AppLayout: Orchestrates sidebar, chat area, execution panels, input, file viewer, terminal panel, and status bar. Manages resizable panels and collapsed states.
- Chat system: Message grouping, display item generation, pending actions, and streaming text handling are centralized in the chat store.
- Terminal system: Integrated XTerm terminal with dynamic theming from CSS custom properties and debounced event handling.
- Blackboard state management: Centralized store with loading/error states and debounced event synchronization.
- Settings modal: Tabbed settings UI backed by a dedicated store.
- UI primitives: Small, theme-aware components (button, dialog, tabs, tooltip) using Tailwind and Radix with consistent icon sizing.

**Section sources**
- [App.tsx:21-91](file://frontend/src/App.tsx#L21-L91)
- [AppLayout.tsx:30-135](file://frontend/src/components/layout/AppLayout.tsx#L30-L135)
- [chatStore.ts:440-571](file://frontend/src/stores/chatStore.ts#L440-L571)
- [useXTermTheme.ts:1-89](file://frontend/src/hooks/useXTermTheme.ts#L1-L89)
- [Terminal.tsx:1-134](file://frontend/src/components/terminal/Terminal.tsx#L1-L134)
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [useBlackboardEvents.ts:1-59](file://frontend/src/hooks/events/useBlackboardEvents.ts#L1-L59)
- [settingsStore.ts:13-20](file://frontend/src/stores/settingsStore.ts#L13-L20)
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)

## Architecture Overview
The frontend integrates tightly with the Wails backend via generated bindings and runtime events. The App component subscribes to backend events and updates global stores. Stores are used to manage chat messages, UI state, settings, and blackboard state. The layout composes specialized panels including the terminal with dynamic theming and enhanced blackboard state management. The terminal theming system provides a seamless dark theme experience with proper ANSI color mapping.

```mermaid
sequenceDiagram
participant Runtime as "Wails Runtime"
participant App as "App.tsx"
participant Store as "Stores"
participant Layout as "AppLayout.tsx"
participant Terminal as "Terminal.tsx"
participant Theme as "useXTermTheme.ts"
Runtime-->>App : "startup_error"
App->>Store : "Set startup error state"
Runtime-->>App : "vector_index : status"
App->>Store : "Update vector index state"
App->>Layout : "Render layout"
Layout->>Store : "Read UI state (sidebar/file viewer/terminal)"
Layout->>Terminal : "Initialize with theme"
Terminal->>Theme : "Resolve CSS variables"
Theme-->>Terminal : "XTerm theme object"
Layout->>Runtime : "Subscribe to events (via hooks)"
```

**Diagram sources**
- [App.tsx:25-55](file://frontend/src/App.tsx#L25-L55)
- [AppLayout.tsx:30-135](file://frontend/src/components/layout/AppLayout.tsx#L30-L135)
- [Terminal.tsx:20-32](file://frontend/src/components/terminal/Terminal.tsx#L20-L32)
- [useXTermTheme.ts:70-89](file://frontend/src/hooks/useXTermTheme.ts#L70-L89)

## Detailed Component Analysis

### Enhanced Terminal Theming System
The terminal theming system provides a sophisticated color mapping mechanism that synchronizes XTerm colors with the application's CSS custom properties. The system resolves theme tokens from CSS variables and applies them consistently across the terminal interface.

Key aspects:
- CSS custom property mapping: The TOKEN_MAP defines how each XTerm theme token maps to specific CSS variables in the @theme section.
- Dynamic theme resolution: The useXTermTheme hook resolves theme values at mount time and caches them for the session lifecycle.
- ANSI color consistency: Colors are mapped to semantic design tokens (destructive, success, info, highlight) ensuring visual consistency.
- Terminal integration: The Terminal component receives the resolved theme and applies it during initialization.

```mermaid
flowchart TD
Start(["Terminal Initialization"]) --> ThemeHook["useXTermTheme hook"]
ThemeHook --> CSSVars["Read CSS Variables"]
CSSVars --> TokenMap["Apply TOKEN_MAP mapping"]
TokenMap --> ResolveColors["Resolve hex values"]
ResolveColors --> CreateTheme["Create ITheme object"]
CreateTheme --> XTermInit["Initialize XTerm with theme"]
XTermInit --> ApplyStyles["Apply terminal styles"]
ApplyStyles --> Ready["Terminal ready with consistent theming"]
```

**Diagram sources**
- [useXTermTheme.ts:43-79](file://frontend/src/hooks/useXTermTheme.ts#L43-L79)
- [Terminal.tsx:26-32](file://frontend/src/components/terminal/Terminal.tsx#L26-L32)
- [index.css:61-71](file://frontend/src/index.css#L61-L71)

**Section sources**
- [useXTermTheme.ts:1-89](file://frontend/src/hooks/useXTermTheme.ts#L1-L89)
- [Terminal.tsx:1-134](file://frontend/src/components/terminal/Terminal.tsx#L1-L134)
- [index.css:61-71](file://frontend/src/index.css#L61-L71)

### Improved Blackboard State Management
The blackboard state management system provides robust state handling with loading indicators, error management, and debounced event synchronization. The system ensures reliable state updates even under rapid event changes.

Key aspects:
- Centralized state store: The blackboardStore maintains state, loading, and error properties with stable selectors.
- Debounced event handling: The useBlackboardEvents hook implements a 300ms debounce to prevent excessive API calls.
- Loading/error states: Comprehensive state management with proper loading indicators and error reporting.
- Event synchronization: Automatic fetching on session changes and cleanup on unmount.

```mermaid
sequenceDiagram
participant Events as "useBlackboardEvents"
participant Store as "blackboardStore"
participant API as "getBlackboardState"
participant Timer as "Debounce Timer"
Events->>Store : "Fetch initial state"
Store->>API : "RPC call"
API-->>Store : "State data"
Store-->>Events : "setState"
Events->>Timer : "Schedule debounce"
Timer->>API : "Delayed fetch"
API-->>Store : "Updated state"
Store-->>Events : "setState"
Events->>Store : "Clear on session change"
```

**Diagram sources**
- [useBlackboardEvents.ts:12-44](file://frontend/src/hooks/events/useBlackboardEvents.ts#L12-L44)
- [blackboardStore.ts:44-53](file://frontend/src/stores/blackboardStore.ts#L44-L53)

**Section sources**
- [blackboardStore.ts:1-54](file://frontend/src/stores/blackboardStore.ts#L1-L54)
- [useBlackboardEvents.ts:1-59](file://frontend/src/hooks/events/useBlackboardEvents.ts#L1-L59)
- [blackboard.ts:1-17](file://frontend/src/api/blackboard.ts#L1-L17)

### Chat Interface Implementation
The chat interface is driven by a rich store that models messages and display items, groups related events into hierarchical structures, and tracks pending actions requiring user input. It supports streaming assistant text, tool calls/results, plan steps, reflections, and context compaction.

Key aspects:
- Message types and display items: The store defines a comprehensive set of message kinds and display item variants to render a unified timeline.
- Grouping algorithm: Messages are grouped into plan steps, thought groups, tool executions, and service notifications. It handles out-of-order tool results and pending actions.
- Streaming and UI state: The store tracks streaming text, thinking state, and per-step context fill metrics.
- Pending actions: Dedicated UI bars surface unresolved tool confirmations, user questions, resume prompts, and step limits.

```mermaid
flowchart TD
Start(["Incoming message"]) --> Type{"Message type?"}
Type --> |plan| BuildPlan["Build step index"]
Type --> |plan_step_start| OpenStep["Open plan step container"]
Type --> |plan_step_complete| CloseStep["Close plan step"]
Type --> |reflection| AddReflection["Add reflection to container"]
Type --> |thought| AddThought["Add thought to buffer"]
Type --> |tool_call| CreateTool["Create tool item<br/>match pending results"]
Type --> |tool_result| MatchTool["Match tool_call_id<br/>apply result"]
Type --> |tool_confirm| PendingTool["Add to pending actions"]
Type --> |ask_user| PendingAsk["Add to pending actions"]
Type --> |task_failed_resumable| PendingResume["Add to pending actions"]
Type --> |step_limit| PendingLimit["Add to pending actions"]
Type --> |status/retry/routing| ServiceMsg["Add service message"]
Type --> |error| ErrorMsg["Add error item"]
Type --> |other| Skip["Skip lifecycle markers"]
OpenStep --> Buffer["Buffer items until completion"]
CloseStep --> Buffer
AddThought --> Collapse["Collapse into thought groups"]
CreateTool --> MatchTool
MatchTool --> Done["Append to items"]
PendingTool --> Done
PendingAsk --> Done
PendingResume --> Done
PendingLimit --> Done
ServiceMsg --> Done
ErrorMsg --> Done
Collapse --> Done
BuildPlan --> Done
Skip --> Done
```

**Diagram sources**
- [chatStore.ts:77-410](file://frontend/src/stores/chatStore.ts#L77-L410)

**Section sources**
- [chatStore.ts:3-41](file://frontend/src/stores/chatStore.ts#L3-L41)
- [chatStore.ts:77-410](file://frontend/src/stores/chatStore.ts#L77-L410)
- [chatStore.ts:440-571](file://frontend/src/stores/chatStore.ts#L440-L571)

### File Viewer System
The file viewer panel is integrated into the main layout and controlled by a dedicated store. The panel supports:
- Collapsed/unexpanded states with a minimal width indicator.
- Resizable width with persisted width stored in the store.
- Toggle controls for expanding/collapsing the panel.
- Integration with the sidebar resizing and overall layout responsiveness.
- File icon caching and consistent icon sizing across the interface.

```mermaid
sequenceDiagram
participant UI as "AppLayout.tsx"
participant Store as "fileViewerStore"
participant Panel as "FileViewerPanel"
UI->>Store : "Read openFiles, collapsed, width"
UI->>Store : "Persist width on change"
UI->>Panel : "Render with width"
UI->>Store : "Toggle collapsed"
Store-->>UI : "Updated collapsed state"
```

**Diagram sources**
- [AppLayout.tsx:30-135](file://frontend/src/components/layout/AppLayout.tsx#L30-L135)

**Section sources**
- [AppLayout.tsx:30-135](file://frontend/src/components/layout/AppLayout.tsx#L30-L135)

### Workspace Panel
The workspace panel resides in the sidebar and is part of the layout composition. It provides project and workspace navigation, file tree, indexing status, and related workspace features. The panel participates in the layout's collapsible behavior and integrates with the project store for active project state.

```mermaid
graph TB
Layout["AppLayout.tsx"] --> Sidebar["Sidebar.tsx"]
Sidebar --> Workspace["WorkspacePanel.tsx"]
Sidebar --> FileTree["FileTreePanel.tsx"]
Sidebar --> Status["IndexingStatus.tsx"]
```

**Diagram sources**
- [AppLayout.tsx:56-90](file://frontend/src/components/layout/AppLayout.tsx#L56-L90)

**Section sources**
- [AppLayout.tsx:56-90](file://frontend/src/components/layout/AppLayout.tsx#L56-L90)

### Settings Modal
The settings modal is a tabbed dialog backed by a store that manages open/closed state and active tab. It surfaces configuration warnings and organizes settings into categories such as general, LLM, search, MCP, security, and about.

```mermaid
sequenceDiagram
participant UI as "SettingsModal.tsx"
participant Store as "settingsStore"
participant Tabs as "Tabs/TabsContent"
UI->>Store : "Open settings (optional tab)"
Store-->>UI : "Set open=true, activeTab"
UI->>Tabs : "Render active tab content"
UI->>Store : "Close settings"
Store-->>UI : "Set open=false"
```

**Diagram sources**
- [SettingsModal.tsx:18-156](file://frontend/src/components/settings/SettingsModal.tsx#L18-L156)
- [settingsStore.ts:13-20](file://frontend/src/stores/settingsStore.ts#L13-L20)

**Section sources**
- [SettingsModal.tsx:18-156](file://frontend/src/components/settings/SettingsModal.tsx#L18-L156)
- [settingsStore.ts:13-20](file://frontend/src/stores/settingsStore.ts#L13-L20)

### UI Component Library
The UI library provides lightweight, theme-consistent primitives with enhanced icon consistency:
- Button: Variants and sizes with Radix slot semantics and improved icon sizing consistency.
- Dialog, Tabs, DropdownMenu, Input, Separator, Tooltip: Composed from Radix UI and styled with Tailwind.
- Utilities: Class merging helpers and variant builders enable consistent styling across components.
- Icon consistency: All components now use consistent icon sizing with the pattern [&_svg]:not([class*='size-']):size-4.

```mermaid
classDiagram
class Button {
+variant
+size
+asChild
+className
}
class Dialog {
+showCloseButton
+children
}
class Tabs {
+orientation
+variant
}
```

**Diagram sources**
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)
- [dialog.tsx:50-82](file://frontend/src/components/ui/dialog.tsx#L50-L82)
- [tabs.tsx:10-27](file://frontend/src/components/ui/tabs.tsx#L10-L27)

**Section sources**
- [button.tsx:8-32](file://frontend/src/components/ui/button.tsx#L8-L32)
- [dialog.tsx:1-159](file://frontend/src/components/ui/dialog.tsx#L1-L159)
- [tabs.tsx:1-78](file://frontend/src/components/ui/tabs.tsx#L1-L78)
- [dropdown-menu.tsx:1-258](file://frontend/src/components/ui/dropdown-menu.tsx#L1-L258)
- [input.tsx:1-22](file://frontend/src/components/ui/input.tsx#L1-L22)
- [button-variants.ts:22-35](file://frontend/src/components/ui/button-variants.ts#L22-L35)
- [index.css:29-57](file://frontend/src/index.css#L29-L57)

### Integration with Wails Backend
The frontend communicates with the Go backend through generated bindings and runtime events:
- useWails hook exposes typed APIs for sessions, projects, file operations, and configuration.
- App listens for startup errors and vector index status events and updates stores accordingly.
- Event payloads conform to typed interfaces for tool calls, results, plan steps, reflections, and context metrics.
- Terminal events are handled with proper base64 encoding for raw PTY byte preservation.

```mermaid
sequenceDiagram
participant App as "App.tsx"
participant Hook as "useWails.ts"
participant Types as "wails.ts"
participant Runtime as "window.runtime"
participant Store as "Stores"
App->>Hook : "useWails()"
Hook-->>App : "{ api, runtime, isReady }"
Runtime-->>App : "EventsOn('startup_error')"
App->>Store : "Set startup error"
Runtime-->>App : "EventsOn('vector_index : status')"
App->>Store : "Update vector index state"
Runtime-->>App : "EventsOn('terminal_output')"
App->>Store : "Process terminal data"
App->>Types : "Validate event payloads"
```

**Diagram sources**
- [App.tsx:21-55](file://frontend/src/App.tsx#L21-L55)
- [useWails.ts:51-61](file://frontend/src/hooks/useWails.ts#L51-L61)
- [wails.ts:32-205](file://frontend/src/lib/wails.ts#L32-L205)
- [Terminal.tsx:51-62](file://frontend/src/components/terminal/Terminal.tsx#L51-L62)

**Section sources**
- [App.tsx:21-55](file://frontend/src/App.tsx#L21-L55)
- [useWails.ts:51-61](file://frontend/src/hooks/useWails.ts#L51-L61)
- [wails.ts:32-205](file://frontend/src/lib/wails.ts#L32-L205)

## Dependency Analysis
The frontend relies on React 19, Zustand for state, Tailwind CSS for styling, and Vite for building. The build configuration aliases the @ path to src and enables React and Tailwind plugins. TypeScript is configured to include the src directory. The terminal theming system adds @xterm/xterm and @xterm/addon-fit as dependencies.

```mermaid
graph TB
Pkg["package.json"] --> React["react@^19"]
Pkg --> ReactDOM["react-dom@^19"]
Pkg --> Zustand["zustand@^5"]
Pkg --> Tailwind["tailwindcss@^4"]
Pkg --> Vite["vite@^6"]
Pkg --> TS["typescript@~5.7"]
Pkg --> XTerm["@xterm/xterm@^1"]
Pkg --> FitAddon["@xterm/addon-fit@^1"]
ViteCfg["vite.config.ts"] --> Alias["@ alias to ./src"]
ViteCfg --> Plugins["Plugins: react, tailwindcss"]
TSConf["tsconfig.app.json"] --> Include["Include: src"]
```

**Diagram sources**
- [package.json:14-61](file://frontend/package.json#L14-L61)
- [vite.config.ts:6-14](file://frontend/vite.config.ts#L6-L14)
- [tsconfig.app.json:1-5](file://frontend/tsconfig.app.json#L1-L5)

**Section sources**
- [package.json:14-61](file://frontend/package.json#L14-L61)
- [vite.config.ts:6-14](file://frontend/vite.config.ts#L6-L14)
- [tsconfig.app.json:1-5](file://frontend/tsconfig.app.json#L1-L5)

## Performance Considerations
- Efficient rendering: The layout uses memoized resize handles and selective re-renders based on store slices.
- Minimal re-renders: Zustand selectors reduce unnecessary updates when only parts of state change.
- Streaming UI: Assistant streaming text is appended incrementally to avoid large DOM updates.
- Event-driven updates: Backend events update stores directly, minimizing polling and redundant computations.
- CSS performance: Tailwind utilities and a compact theme reduce CSS bloat; custom prose variants optimize markdown rendering.
- Terminal optimization: XTerm theme resolution occurs once at mount, reducing repeated calculations.
- Debounced blackboard updates: 300ms debounce prevents excessive API calls during rapid state changes.
- Icon caching: File icon system caches results to avoid repeated API calls.

## Troubleshooting Guide
Common issues and remedies:
- Startup errors: The App component displays a dismissible banner when the backend emits a startup error. Inspect the message and error fields to diagnose initialization problems.
- Vector index status: Subscribe to vector index events to monitor indexing progress and detect failures.
- Settings persistence: Ensure the settings store is initialized and that tab changes persist across sessions.
- Terminal theming issues: Verify that CSS custom properties are properly defined in the @theme section and accessible to the useXTermTheme hook.
- Blackboard state synchronization: Check for proper event debouncing and ensure the store clears state on session changes.
- API key masking: Some providers mask API keys in settings; verify masked values and update configurations as needed.
- Accessibility: The theme removes default focus rings; ensure alternative focus indicators remain visible for keyboard navigation.
- Icon consistency: All components now use consistent icon sizing; verify that custom icons follow the [&_svg]:not([class*='size-']):size-4 pattern.

**Section sources**
- [App.tsx:61-87](file://frontend/src/App.tsx#L61-L87)
- [api.ts:1-3](file://frontend/src/constants/api.ts#L1-L3)
- [useXTermTheme.ts:34-36](file://frontend/src/hooks/useXTermTheme.ts#L34-L36)
- [useBlackboardEvents.ts:10](file://frontend/src/hooks/events/useBlackboardEvents.ts#L10)
- [button-variants.ts:25](file://frontend/src/components/ui/button-variants.ts#L25)

## Conclusion
C0WRK's frontend combines a modular React 19 architecture with a robust store-based state model, a cohesive UI component library, and seamless Wails integration. The enhanced terminal theming system provides consistent dark theme experience across the terminal interface. The improved blackboard state management ensures reliable state synchronization with proper loading and error handling. The chat interface, file viewer, workspace panel, and settings modal are designed for clarity, responsiveness, and cross-platform consistency. The build and styling stack ensures maintainability and performance across platforms.

## Appendices

### Build System and Configuration
- Vite: React plugin, Tailwind plugin, and path aliasing simplify development and production builds.
- TypeScript: Strict app configuration scoped to src for accurate type checking.
- Styles: Tailwind theme tokens and prose overrides align UI with the dark theme.
- Terminal dependencies: XTerm and Fit addon provide robust terminal emulation with dynamic theming.

**Section sources**
- [vite.config.ts:6-14](file://frontend/vite.config.ts#L6-L14)
- [tsconfig.app.json:1-5](file://frontend/tsconfig.app.json#L1-L5)
- [index.css:29-57](file://frontend/src/index.css#L29-L57)
- [package.json:33-40](file://frontend/package.json#L33-L40)

### Responsive Design and Accessibility
- Responsive layout: Flexbox-based layout adapts to screen size; resizable panels maintain usability.
- Accessibility: Focus management and keyboard navigation are supported; ensure custom focus styles complement the theme.
- Cross-platform UI: Consistent styling and component behavior across operating systems via Tailwind and Radix.
- Icon consistency: All UI components now use standardized icon sizing for better visual consistency.

**Section sources**
- [AppLayout.tsx:57-135](file://frontend/src/components/layout/AppLayout.tsx#L57-L135)
- [index.css:59-62](file://frontend/src/index.css#L59-L62)
- [button-variants.ts:25](file://frontend/src/components/ui/button-variants.ts#L25)

### Terminal Theming Implementation Details
The terminal theming system provides a sophisticated bridge between CSS custom properties and XTerm color schemes:
- CSS custom property mapping: Each XTerm theme token maps to specific design tokens in the @theme section.
- Dynamic resolution: Theme values are resolved at mount time and cached for the session lifecycle.
- ANSI color consistency: Colors are mapped to semantic design tokens ensuring visual consistency across the application.
- Terminal integration: The Terminal component receives the resolved theme and applies it during initialization.

**Section sources**
- [useXTermTheme.ts:43-64](file://frontend/src/hooks/useXTermTheme.ts#L43-L64)
- [useXTermTheme.ts:70-79](file://frontend/src/hooks/useXTermTheme.ts#L70-L79)
- [Terminal.tsx:26-32](file://frontend/src/components/terminal/Terminal.tsx#L26-L32)
- [index.css:61-71](file://frontend/src/index.css#L61-L71)

### Blackboard State Management Architecture
The blackboard state management system provides robust state handling with comprehensive error management:
- Centralized store: Maintains state, loading, and error properties with stable selectors for efficient updates.
- Debounced event handling: Implements 300ms debounce to prevent excessive API calls during rapid state changes.
- Loading/error states: Provides comprehensive state management with proper loading indicators and error reporting.
- Event synchronization: Automatically fetches state on session changes and cleans up timers on unmount.

**Section sources**
- [blackboardStore.ts:44-53](file://frontend/src/stores/blackboardStore.ts#L44-L53)
- [useBlackboardEvents.ts:10](file://frontend/src/hooks/events/useBlackboardEvents.ts#L10)
- [useBlackboardEvents.ts:47-58](file://frontend/src/hooks/events/useBlackboardEvents.ts#L47-L58)