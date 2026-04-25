# File Viewer Components

<cite>
**Referenced Files in This Document**
- [FileViewerPanel.tsx](file://frontend/src/components/fileViewer/FileViewerPanel.tsx)
- [FileViewerTabBar.tsx](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx)
- [FileViewerContent.tsx](file://frontend/src/components/fileViewer/FileViewerContent.tsx)
- [fileViewerStore.ts](file://frontend/src/stores/fileViewerStore.ts)
- [cmDiffDecorations.ts](file://frontend/src/lib/cmDiffDecorations.ts)
- [cmLanguages.ts](file://frontend/src/lib/cmLanguages.ts)
- [cmTheme.ts](file://frontend/src/lib/cmTheme.ts)
- [diffParser.ts](file://frontend/src/lib/diffParser.ts)
- [markdownConfig.tsx](file://frontend/src/lib/markdownConfig.tsx)
- [fileViewerUtils.ts](file://frontend/src/lib/fileViewerUtils.ts)
- [hljsLanguages.ts](file://frontend/src/lib/hljsLanguages.ts)
- [AppLayout.tsx](file://frontend/src/components/layout/AppLayout.tsx)
- [FileTreePanel.tsx](file://frontend/src/components/layout/FileTreePanel.tsx)
- [WorkspacePanel.tsx](file://frontend/src/components/layout/WorkspacePanel.tsx)
</cite>

## Update Summary
**Changes Made**
- Complete replacement of highlight.js implementation with CodeMirror 6 for syntax highlighting and rendering
- New CodeMirror-based file content rendering with custom diff decorations and theme integration
- Updated language detection system using @codemirror/language-data
- Replaced highlight.js decorations with CodeMirror state fields and effects
- Added new CodeMirror theme system with CSS variable integration
- Updated file type detection to use CodeMirror language descriptions

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
This document describes the file viewer system in C0WRK, focusing on three primary UI components: FileViewerPanel as the main container, FileViewerTabBar for tab management, and FileViewerContent for rendering file content. The system has been completely rewritten to use CodeMirror 6 for enhanced syntax highlighting, line numbering, inline diffs, and Markdown rendering. It explains how file type detection, custom diff decorations, theme integration, and CodeMirror language support work together, along with integration points to the workspace system and persistence. It also covers customization options for different file types, performance optimizations for large files, and accessibility features for code reading.

## Project Structure
The file viewer now uses CodeMirror 6 as the primary rendering engine with custom decorations and theme integration. The system integrates with the global application layout and workspace panels while maintaining the same component architecture.

```mermaid
graph TB
subgraph "File Viewer"
P["FileViewerPanel.tsx"]
T["FileViewerTabBar.tsx"]
C["FileViewerContent.tsx"]
end
subgraph "Stores"
S["fileViewerStore.ts"]
end
subgraph "CodeMirror Libraries"
CD["cmDiffDecorations.ts"]
CL["cmLanguages.ts"]
CT["cmTheme.ts"]
end
subgraph "Legacy Support"
HL["hljsLanguages.ts"]
end
subgraph "Layout"
AL["AppLayout.tsx"]
WP["WorkspacePanel.tsx"]
FTP["FileTreePanel.tsx"]
end
P --> T
P --> C
C --> CD
C --> CL
C --> CT
C --> S
T --> S
C --> S
S --> AL
AL --> P
AL --> FTP
FTP --> S
WP --> FTP
```

**Diagram sources**
- [FileViewerPanel.tsx:1-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L1-L37)
- [FileViewerTabBar.tsx:1-166](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L1-L166)
- [FileViewerContent.tsx:1-278](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L1-L278)
- [fileViewerStore.ts:1-207](file://frontend/src/stores/fileViewerStore.ts#L1-L207)
- [cmDiffDecorations.ts:1-208](file://frontend/src/lib/cmDiffDecorations.ts#L1-L208)
- [cmLanguages.ts:1-81](file://frontend/src/lib/cmLanguages.ts#L1-L81)
- [cmTheme.ts:1-113](file://frontend/src/lib/cmTheme.ts#L1-L113)
- [hljsLanguages.ts:1-49](file://frontend/src/lib/hljsLanguages.ts#L1-L49)
- [AppLayout.tsx:24-54](file://frontend/src/components/layout/AppLayout.tsx#L24-L54)
- [WorkspacePanel.tsx:1-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L1-L71)
- [FileTreePanel.tsx:1-34](file://frontend/src/components/layout/FileTreePanel.tsx#L1-L34)

**Section sources**
- [FileViewerPanel.tsx:1-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L1-L37)
- [AppLayout.tsx:24-54](file://frontend/src/components/layout/AppLayout.tsx#L24-L54)

## Core Components
- FileViewerPanel: Container that renders tabs and content, hides itself when no files are open or collapsed, and applies width from layout.
- FileViewerTabBar: Manages open files as tabs, supports switching, closing, scrolling active tab into view, and a dropdown menu for all open files.
- FileViewerContent: Renders active file content using CodeMirror 6 with syntax highlighting, line numbers, Markdown preview/raw toggle, inline diffs, and binary file handling.

Key integration points:
- Uses fileViewerStore for open files, active file, and panel state.
- Reads file content and diffs via backend APIs exposed to the frontend.
- Integrates with workspace events to refresh content silently on external changes.
- Implements CodeMirror 6 editor view with custom decorations and theme system.

**Section sources**
- [FileViewerPanel.tsx:7-36](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L36)
- [FileViewerTabBar.tsx:31-165](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L31-L165)
- [FileViewerContent.tsx:23-103](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L23-L103)
- [fileViewerStore.ts:53-206](file://frontend/src/stores/fileViewerStore.ts#L53-L206)

## Architecture Overview
The file viewer is now powered by CodeMirror 6, providing enhanced syntax highlighting, custom diff decorations, and theme integration. The system maintains the same reactive architecture with Zustand store integration while leveraging CodeMirror's advanced editor capabilities.

```mermaid
sequenceDiagram
participant UI as "FileViewerPanel"
participant Tabs as "FileViewerTabBar"
participant Content as "FileViewerContent"
participant Store as "fileViewerStore"
participant CM as "CodeMirror 6"
participant Backend as "window.go.desktop.App"
UI->>Tabs : Render tabs for openFiles
UI->>Content : Render active file content
Content->>Store : Read activeFile/openFiles
Content->>Backend : ReadFile(path)
Content->>Backend : GetFileDiff(path)
Backend-->>Content : {content, diff}
Content->>Store : Update content, diff, isBinary, isLoading
Content->>CM : Create EditorView with decorations
Content->>CM : Apply language support and theme
Store-->>UI : Subscribe to state changes
UI-->>Tabs : Re-render tabs
UI-->>Content : Re-render content
```

**Diagram sources**
- [FileViewerPanel.tsx:7-36](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L36)
- [FileViewerTabBar.tsx:31-165](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L31-L165)
- [FileViewerContent.tsx:23-103](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L23-L103)
- [fileViewerStore.ts:53-206](file://frontend/src/stores/fileViewerStore.ts#L53-L206)

## Detailed Component Analysis

### FileViewerPanel
- Purpose: Main container for the file viewer area. It hides itself when no files are open or when collapsed, and applies a dynamic width from the layout.
- Responsibilities:
  - Conditionally render tabs and content based on open files and collapse state.
  - Pass width to child components for responsive sizing.
- Integration:
  - Subscribes to openTabs and collapsed state from the store.
  - Renders FileViewerTabBar and FileViewerContent.

```mermaid
flowchart TD
Start(["Render FileViewerPanel"]) --> CheckOpen["openTabs.length === 0?"]
CheckOpen --> |Yes| Hide["Return null (hidden)"]
CheckOpen --> |No| CheckCollapse["collapsed?"]
CheckCollapse --> |Yes| Hide
CheckCollapse --> |No| Render["Render <FileViewerTabBar/> + <FileViewerContent/>"]
Render --> End(["Done"])
Hide --> End
```

**Diagram sources**
- [FileViewerPanel.tsx:7-36](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L36)

**Section sources**
- [FileViewerPanel.tsx:7-36](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L7-L36)
- [AppLayout.tsx:24-54](file://frontend/src/components/layout/AppLayout.tsx#L24-L54)

### FileViewerTabBar
- Purpose: Tab bar for managing multiple open files.
- Features:
  - Displays file icons and names.
  - Active tab highlighting and hover states.
  - Close buttons per tab with accessible labels.
  - Auto-scroll active tab into view.
  - Dropdown menu listing all open files with "active" indicator.
  - Collapse button to minimize the inspector.
- Interactions:
  - Click tab to switch active file.
  - Click close to close a file.
  - Use dropdown to quickly navigate among open files.

```mermaid
sequenceDiagram
participant User as "User"
participant Bar as "FileViewerTabBar"
participant Store as "fileViewerStore"
User->>Bar : Click tab
Bar->>Store : setActiveFile(path)
Store-->>Bar : activeFile updated
Bar->>Bar : scrollToTab(path)
User->>Bar : Click close button
Bar->>Store : closeFile(path)
Store-->>Bar : openTabs updated
```

**Diagram sources**
- [FileViewerTabBar.tsx:31-165](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L31-L165)
- [fileViewerStore.ts:64-122](file://frontend/src/stores/fileViewerStore.ts#L64-L122)

**Section sources**
- [FileViewerTabBar.tsx:31-165](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L31-L165)
- [fileViewerStore.ts:64-122](file://frontend/src/stores/fileViewerStore.ts#L64-L122)

### FileViewerContent
- Purpose: Renders the active file's content using CodeMirror 6 with enhanced syntax highlighting, line numbers, and optional Markdown preview.
- Highlights:
  - CodeMirror 6 editor view with syntax highlighting via language support.
  - Line-by-line rendering with numbers and custom theme integration.
  - Inline diffs using custom decorations with added/removed/modified styling.
  - Markdown preview with GitHub-flavored markdown and sanitization.
  - Binary file detection and graceful handling.
  - Loading and error states with proper cleanup.
  - Scroll position preservation during content updates.
  - Dynamic language loading and theme switching.
- File type detection and rendering:
  - Language detection using @codemirror/language-data with fallback mappings.
  - Markdown-specific preview/raw toggle with CodeMirror integration.
  - Unified diff parsing and display line construction with custom decorations.
- Backend integration:
  - Reads file content and diff via Wails-bound APIs.
  - Subscribes to workspace tree changes to silently refresh content.

**Updated** Complete rewrite using CodeMirror 6 with custom decorations and theme system

```mermaid
flowchart TD
Start(["Render FileViewerContent"]) --> CheckActive["activeFile exists?"]
CheckActive --> |No| Null["Return null"]
CheckActive --> |Yes| CheckLoading["isLoading?"]
CheckLoading --> |Yes| Loading["Show loader"]
CheckLoading --> |No| CheckError["error?"]
CheckError --> |Yes| ShowError["Show error message"]
CheckError --> |No| CheckBinary["isBinary?"]
CheckBinary --> |Yes| BinaryMsg["Show unsupported format"]
CheckBinary --> |No| DecideMode["Markdown and not raw?"]
DecideMode --> |Yes| Markdown["Render ReactMarkdown with plugins"]
DecideMode --> |No| CodeMirror["Render CodeMirror EditorView<br/>with decorations and theme"]
CodeMirror --> Diffs{"Has diff?"}
Diffs --> |Yes| DiffDecorations["Apply custom diff decorations"]
Diffs --> |No| PlainCM["Apply language support and theme"]
Markdown --> End(["Done"])
DiffDecorations --> End
PlainCM --> End
Loading --> End
ShowError --> End
BinaryMsg --> End
Null --> End
```

**Diagram sources**
- [FileViewerContent.tsx:23-103](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L23-L103)
- [FileViewerContent.tsx:107-277](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L107-L277)
- [cmDiffDecorations.ts:57-108](file://frontend/src/lib/cmDiffDecorations.ts#L57-L108)
- [cmTheme.ts:17-112](file://frontend/src/lib/cmTheme.ts#L17-L112)
- [markdownConfig.tsx:55-92](file://frontend/src/lib/markdownConfig.tsx#L55-L92)

**Section sources**
- [FileViewerContent.tsx:23-103](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L23-L103)
- [FileViewerContent.tsx:107-277](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L107-L277)
- [fileViewerStore.ts:53-206](file://frontend/src/stores/fileViewerStore.ts#L53-L206)

### CodeMirror-Based Rendering System
- **EditorView Creation**: Creates a CodeMirror 6 EditorView with read-only state, line numbers, and custom theme.
- **Language Support**: Dynamically loads languages via @codemirror/language-data with fallback mappings for special files.
- **Custom Decorations**: Implements state fields for diff decorations and line highlighting using CodeMirror's effect system.
- **Theme Integration**: One Dark theme created from CSS variables for consistent design system integration.
- **Performance Optimizations**: Uses compartments for language switching, memoized themes, and incremental updates.

**Section sources**
- [FileViewerContent.tsx:124-154](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L124-L154)
- [cmLanguages.ts:41-80](file://frontend/src/lib/cmLanguages.ts#L41-L80)
- [cmDiffDecorations.ts:9-207](file://frontend/src/lib/cmDiffDecorations.ts#L9-L207)
- [cmTheme.ts:17-112](file://frontend/src/lib/cmTheme.ts#L17-L112)

### File Type Detection and Customization
- **Language Detection**:
  - Uses @codemirror/language-data LanguageDescription.matchFilename for primary detection.
  - Includes fallback mappings for special filenames (Makefile, Dockerfile, .gitignore, etc.).
  - Handles extension-based fallbacks for formats like TOML, INI, and configuration files.
- **Dynamic Language Loading**:
  - Asynchronously loads language support via loadLanguageByName function.
  - Uses CodeMirror's compartment system for efficient language switching.
  - Falls back to plaintext for unknown or unsupported languages.
- **Markdown Rendering**:
  - GitHub Flavored Markdown with emoji, breaks, slug generation, autolinking, external links, sanitization, and Mermaid diagrams.
  - Toggle between preview and raw source modes.
- **Customization Options**:
  - Extend FILENAME_FALLBACK and EXT_FALLBACK maps for new file types.
  - Add new CodeMirror language support via language-data integration.
  - Customize theme colors by modifying CSS variables.

**Updated** Complete rewrite using CodeMirror language detection and dynamic loading

```mermaid
flowchart TD
Name["Input: fileName"] --> Exact["Exact filename fallback map"]
Exact --> |Found| Lang["Return mapped language"]
Exact --> |Not Found| Ext["Extract extension(s)"]
Ext --> ExtMap["Extension fallback map"]
ExtMap --> |Found| Lang
ExtMap --> |Not Found| LangData["LanguageDescription.matchFilename"]
LangData --> |Found| Lang
LangData --> |Not Found| Plaintext["Return 'text/plain'"]
```

**Diagram sources**
- [cmLanguages.ts:41-61](file://frontend/src/lib/cmLanguages.ts#L41-L61)

**Section sources**
- [cmLanguages.ts:9-61](file://frontend/src/lib/cmLanguages.ts#L9-L61)
- [cmLanguages.ts:70-80](file://frontend/src/lib/cmLanguages.ts#L70-L80)
- [markdownConfig.tsx:17-44](file://frontend/src/lib/markdownConfig.tsx#L17-L44)
- [markdownConfig.tsx:55-92](file://frontend/src/lib/markdownConfig.tsx#L55-L92)

### Custom Diff Decorations System
- **State Management**: Uses CodeMirror StateField and StateEffect for efficient diff decoration updates.
- **Decoration Types**:
  - Added lines: Line decoration with cm-diff-added class.
  - Modified lines: Line decoration with cm-diff-modified class and character-level diffs.
  - Removed lines: Block widget with cm-diff-removed-widget class positioned before next document line.
  - Normal lines: No decoration.
- **Character-Level Diff**: Computes word-level differences using diffWords and applies cm-diff-char-added marks.
- **Widget Implementation**: Custom RemovedLineWidget renders deleted content with proper styling and positioning.
- **Performance**: Efficiently maps decorations through document changes and maintains position accuracy.

**Updated** Complete rewrite using CodeMirror's state field and effect system

```mermaid
flowchart TD
Input["DisplayLine[] from diffParser"] --> Builder["convertToDecorations"]
Builder --> Added["Add line decorations for added/modified"]
Builder --> Removed["Add block widgets for removed lines"]
Builder --> CharDiff["Add character-level marks for modified lines"]
Added --> DecoSet["Return DecorationSet"]
Removed --> DecoSet
CharDiff --> DecoSet
DecoSet --> Apply["Apply via StateField.update"]
Apply --> View["Render in EditorView"]
```

**Diagram sources**
- [cmDiffDecorations.ts:57-108](file://frontend/src/lib/cmDiffDecorations.ts#L57-L108)
- [cmDiffDecorations.ts:14-40](file://frontend/src/lib/cmDiffDecorations.ts#L14-L40)
- [diffParser.ts:127-175](file://frontend/src/lib/diffParser.ts#L127-L175)

**Section sources**
- [cmDiffDecorations.ts:9-207](file://frontend/src/lib/cmDiffDecorations.ts#L9-L207)
- [diffParser.ts:48-175](file://frontend/src/lib/diffParser.ts#L48-L175)

### Theme Integration System
- **CSS Variable Integration**: Theme colors resolved from CSS custom properties (--color-foreground, --color-hljs-comment, etc.).
- **One Dark Theme**: Custom theme implementation matching the application's design system.
- **Syntax Highlighting**: Uses HighlightStyle with Lezer tags for consistent color mapping.
- **Editor Styling**: Custom EditorView theme for fonts, colors, gutters, and scrollbars.
- **Dynamic Updates**: Theme applied as CodeMirror extension, integrated with the store's theme preferences.

**Updated** New CodeMirror theme system replacing highlight.js styling

```mermaid
flowchart TD
CSSVars["CSS Custom Properties"] --> Theme["createOneDarkCMTheme()"]
Theme --> EditorView["EditorView.theme()"]
Theme --> HighlightStyle["HighlightStyle.define()"]
EditorView --> Extensions["Theme Extensions"]
HighlightStyle --> Extensions
Extensions --> CodeMirror["Applied to EditorView"]
```

**Diagram sources**
- [cmTheme.ts:6-112](file://frontend/src/lib/cmTheme.ts#L6-L112)

**Section sources**
- [cmTheme.ts:17-112](file://frontend/src/lib/cmTheme.ts#L17-L112)

### Integration with Workspace System
- **File Opening**: Opens files via the file tree panel and persists state to local storage.
- **Silent Refresh**: On workspace tree changes, content is refreshed without visual loading indicators to preserve scroll position.
- **Workspace Filtering**: File tree supports glob and regex filtering for efficient navigation.
- **CodeMirror Integration**: Uses CodeMirror's built-in subscription system for workspace events.

**Updated** Enhanced integration with CodeMirror's event system

```mermaid
sequenceDiagram
participant Tree as "FileTreePanel"
participant Store as "fileViewerStore"
participant Backend as "window.go.desktop.App"
participant Layout as "AppLayout"
Tree->>Store : openFile(path, name)
Store->>Backend : ReadFile(path)
Store->>Backend : GetFileDiff(path)
Backend-->>Store : {content, diff}
Store-->>Layout : Persist state and panel width
note over Backend,Store : workspace : tree_changed event triggers silentRefreshAllFiles
```

**Diagram sources**
- [FileTreePanel.tsx:1-34](file://frontend/src/components/layout/FileTreePanel.tsx#L1-L34)
- [fileViewerStore.ts:64-122](file://frontend/src/stores/fileViewerStore.ts#L64-L122)
- [fileViewerStore.ts:58-65](file://frontend/src/stores/fileViewerStore.ts#L58-L65)
- [AppLayout.tsx:24-54](file://frontend/src/components/layout/AppLayout.tsx#L24-L54)

**Section sources**
- [fileViewerStore.ts:64-122](file://frontend/src/stores/fileViewerStore.ts#L64-L122)
- [fileViewerStore.ts:58-65](file://frontend/src/stores/fileViewerStore.ts#L58-L65)
- [FileTreePanel.tsx:1-34](file://frontend/src/components/layout/FileTreePanel.tsx#L1-L34)
- [WorkspacePanel.tsx:26-71](file://frontend/src/components/layout/WorkspacePanel.tsx#L26-L71)

## Dependency Analysis
- FileViewerPanel depends on FileViewerTabBar and FileViewerContent and subscribes to fileViewerStore.
- FileViewerTabBar depends on fileViewerStore for open files and actions.
- FileViewerContent depends on:
  - fileViewerStore for active file and state.
  - cmLanguages for CodeMirror language detection and dynamic loading.
  - cmDiffDecorations for custom diff rendering and state management.
  - cmTheme for CodeMirror theme integration.
  - diffParser for unified diff parsing and display line construction.
  - markdownConfig for Markdown rendering and sanitization.
  - fileViewerUtils for binary content detection.
- Stores depend on Wails-bound backend APIs for file read and diff retrieval.
- **Legacy Support**: hljsLanguages.ts remains for backward compatibility but is no longer actively used.

**Updated** Complete dependency rewrite with CodeMirror 6 libraries

```mermaid
graph LR
Panel["FileViewerPanel"] --> TabBar["FileViewerTabBar"]
Panel --> Content["FileViewerContent"]
Content --> Store["fileViewerStore"]
TabBar --> Store
Content --> CL["cmLanguages"]
Content --> CD["cmDiffDecorations"]
Content --> CT["cmTheme"]
Content --> DP["diffParser"]
Content --> MC["markdownConfig"]
Content --> FU["fileViewerUtils"]
Store --> Backend["window.go.desktop.App"]
```

**Diagram sources**
- [FileViewerPanel.tsx:1-37](file://frontend/src/components/fileViewer/FileViewerPanel.tsx#L1-L37)
- [FileViewerTabBar.tsx:1-166](file://frontend/src/components/fileViewer/FileViewerTabBar.tsx#L1-L166)
- [FileViewerContent.tsx:1-278](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L1-L278)
- [fileViewerStore.ts:1-207](file://frontend/src/stores/fileViewerStore.ts#L1-L207)
- [cmLanguages.ts:1-81](file://frontend/src/lib/cmLanguages.ts#L1-L81)
- [cmDiffDecorations.ts:1-208](file://frontend/src/lib/cmDiffDecorations.ts#L1-L208)
- [cmTheme.ts:1-113](file://frontend/src/lib/cmTheme.ts#L1-L113)
- [diffParser.ts:1-176](file://frontend/src/lib/diffParser.ts#L1-L176)
- [markdownConfig.tsx:1-94](file://frontend/src/lib/markdownConfig.tsx#L1-L94)
- [fileViewerUtils.ts:1-18](file://frontend/src/lib/fileViewerUtils.ts#L1-L18)

**Section sources**
- [fileViewerStore.ts:53-206](file://frontend/src/stores/fileViewerStore.ts#L53-L206)

## Performance Considerations
- **CodeMirror 6 Benefits**:
  - Efficient DOM updates through ProseMirror's state system.
  - Incremental rendering with proper document change tracking.
  - Dynamic language loading reduces initial bundle size.
  - Custom decorations system optimized for diff rendering.
- **Asynchronous Loading**:
  - File content and diffs are fetched concurrently when opening a file.
  - Languages loaded asynchronously via CodeMirror's compartment system.
- **Silent Refresh**:
  - On workspace changes, content is refreshed without showing loaders.
- **Binary Detection**:
  - Early detection of binary content prevents heavy processing.
- **Scroll Preservation**:
  - Saved scroll position is restored after content updates.
- **Large File Rendering**:
  - CodeMirror's virtual scrolling handles large files efficiently.
  - Line-by-line rendering with proper document change tracking.
- **Theme Optimization**:
  - Themes memoized to avoid unnecessary re-renders.
  - CSS variable integration for dynamic theme switching.

**Updated** Performance improvements from CodeMirror 6 migration

Recommendations:
- Consider CodeMirror's built-in virtual scrolling for extremely large files.
- Monitor compartment reconfiguration performance for frequent language switching.
- Use debounced workspace refresh events to avoid frequent reloads.
- Leverage CodeMirror's built-in diff rendering for better performance than previous implementations.

**Section sources**
- [fileViewerStore.ts:58-65](file://frontend/src/stores/fileViewerStore.ts#L58-L65)
- [fileViewerStore.ts:58-65](file://frontend/src/stores/fileViewerStore.ts#L58-L65)
- [FileViewerContent.tsx:169-187](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L169-L187)
- [FileViewerContent.tsx:209-238](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L209-L238)

## Troubleshooting Guide
Common issues and resolutions:
- **File does not appear in the viewer**:
  - Ensure the file was opened via the workspace and that the path is within the active project.
  - Check for errors returned by the backend read operation.
- **Binary file shows "unsupported format"**:
  - Binary detection scans the first 8KB for null bytes; this is expected behavior.
- **Markdown preview not rendering**:
  - Verify that the file is detected as Markdown and that the preview toggle is enabled.
  - Inspect sanitized HTML and plugin configuration.
- **Tabs not updating after workspace changes**:
  - Confirm that the workspace tree changed event is firing and silent refresh is invoked.
- **Scroll jumps when content updates**:
  - Scroll preservation relies on CodeMirror's built-in scroll management.
- **Language not highlighting correctly**:
  - Check that the language is available in @codemirror/language-data or fallback mappings.
  - Verify that the language compartment is properly reconfigured.
- **Diff decorations not appearing**:
  - Ensure diff parsing succeeds and display lines are generated.
  - Check that the diff decoration field is properly applied to the EditorView.
- **Theme not applying correctly**:
  - Verify CSS variables are properly defined in the design system.
  - Check that the theme is applied as a CodeMirror extension.

**Updated** Issues specific to CodeMirror 6 implementation

**Section sources**
- [fileViewerStore.ts:64-78](file://frontend/src/stores/fileViewerStore.ts#L64-L78)
- [fileViewerStore.ts:160-168](file://frontend/src/stores/fileViewerStore.ts#L160-L168)
- [FileViewerContent.tsx:34-42](file://frontend/src/components/fileViewer/FileViewerContent.tsx#L34-L42)
- [fileViewerStore.ts:58-65](file://frontend/src/stores/fileViewerStore.ts#L58-L65)
- [cmLanguages.ts:70-80](file://frontend/src/lib/cmLanguages.ts#L70-L80)
- [cmDiffDecorations.ts:160-178](file://frontend/src/lib/cmDiffDecorations.ts#L160-L178)
- [cmTheme.ts:17-112](file://frontend/src/lib/cmTheme.ts#L17-L112)

## Conclusion
C0WRK's file viewer system has been completely rewritten with CodeMirror 6, providing a modern, performant, and feature-rich solution for viewing and navigating files within the workspace. The new implementation offers enhanced syntax highlighting, custom diff decorations, theme integration, and improved performance characteristics. The modular components—panel, tabs, and content—work together with a centralized store to deliver responsive rendering, dynamic language support, inline diffs, and Markdown previews. Integration with the workspace ensures seamless updates, while the new CodeMirror-based architecture provides better maintainability and extensibility. The system is designed to be customizable for new file types and optimized for performance, especially with large files, representing a significant improvement over the previous highlight.js-based implementation.