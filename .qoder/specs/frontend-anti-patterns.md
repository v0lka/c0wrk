# Frontend Anti-Patterns & Clean Implementation Guideline

Catalogue of hacks, ad-hoc solutions, CSS workarounds, and architectural smells
found in the prototype frontend. The clean reimplementation **MUST NOT** carry
any of these over. Each section describes the problem, where it occurs in the
prototype, and the recommended approach for the clean version.

---

## 1. CSS & Styling

### 1.1 Global `!important` focus ring removal

**Prototype:** `index.css:183-188` — A blanket rule kills all focus outlines on
every element in the application.

```css
*:focus,
*:focus-visible,
*:focus-within {
  outline: none !important;
  box-shadow: none !important;
}
```

**Problem:** Destroys keyboard accessibility. `!important` prevents any
component from restoring focus indication.

**Guideline:** Never use `!important` for global reset. Remove default focus
rings per-component via Tailwind's `focus-visible:ring-*` utilities, and always
provide a visible focus indicator (e.g. a subtle `ring-1 ring-ring` or
`outline-offset`) on interactive elements. Accessibility requires a visible
focus state.

---

### 1.2 Force-overriding Radix UI internals with `!important`

**Prototype:** `index.css:191-202` — Radix DropdownMenu highlight/focus
colors overridden via `[data-radix-dropdown-menu-item]` selectors with
`!important`.

**Problem:** Fighting the library's internal DOM. Breaks when Radix changes
data-attribute naming. Prevents component-level customization.

**Guideline:** Style Radix primitives through the `className` prop passed by
shadcn/ui wrappers. Theme-level color overrides belong in the shadcn component
definition (`dropdown-menu.tsx`), not in global CSS targeting internal
data-attributes.

---

### 1.3 Hardcoded hex colors bypassing design tokens

**Prototype:** `ChatInput.tsx:171,181` — Send/cancel buttons use raw hex:
`border-[#be5046]`, `bg-[#98c379]`, `text-[#282c34]`.

**Problem:** These colors exist as theme tokens (`--color-destructive`,
`--color-success`, `--color-background`) but are bypassed. Any theme change
requires hunting through components.

**Guideline:** Never use raw hex in component code. All colors must reference
CSS custom properties via Tailwind classes (`text-destructive`, `bg-success`,
`text-background`). If a color doesn't exist as a token, add it to `@theme`.

---

### 1.4 Duplicated highlight.js theme

**Prototype:** `index.css:3` imports `atom-one-dark.min.css`, then
`index.css:64-118` manually redefines the entire theme.

**Problem:** Two sources of truth for the same colors. If either is modified
independently, they diverge silently.

**Guideline:** Import the theme file OR define custom overrides, never both.
If customization is needed, don't import the distributed CSS; define the full
theme once in `@theme` or a dedicated `hljs-theme.css` file.

---

### 1.5 Prose overrides with hardcoded hex values

**Prototype:** `index.css:137-180` — Tailwind Typography prose variables use
raw hex values (`--tw-prose-body: #abb2bf`) instead of referencing `@theme`
custom properties.

**Guideline:** Prose variables must reference the theme tokens:
`--tw-prose-body: var(--color-foreground)`. This keeps the typography plugin
consistent with the rest of the theme automatically.

---

### 1.6 Direct `document.body.style` mutation

**Prototype:** `useResize.tsx:81-82` — Sets `document.body.style.cursor`
and `document.body.style.userSelect` directly during drag.

**Problem:** Imperative DOM mutation outside React's control. If the component
unmounts mid-drag, stale styles can persist (the cleanup exists but is
defensive).

**Guideline:** Add/remove a CSS class on `<body>` or `<html>` (e.g.
`is-resizing`) that activates the cursor/userSelect styles via CSS. Or use a
`<style>` tag injected via React portal.

---

### 1.7 Inline `style` for dynamic overflow/height

**Prototype:** `ChatInput.tsx:156` — `style={{ overflow: ... }}` computed
from `text.split('\n').length`. `UserMessage.tsx:78-79` — `style={{
maxHeight, overflow }}`.

**Guideline:** For simple boolean toggles, prefer `className` with conditional
Tailwind classes: `overflow-hidden` vs `overflow-auto`. For truly dynamic
numeric values (like `maxHeight` from a calculation), `style` is acceptable
but should be isolated in a single place, not scattered. Consider a custom
utility class generated via CSS `var()`.

---

## 2. State Management

### 2.1 `getState()` abuse outside React

**Prototype:** Dozens of `useChatStore.getState()`, `useSessionStore.getState()`,
`useProjectStore.getState()` calls inside event handlers, callbacks, and even
conditionals:

- `Sidebar.tsx:250,303`
- `useSessionEvents.ts` (throughout — every event handler)
- `ChatInput.tsx:59,91`
- `ToolConfirmation.tsx:60-67,70-74`

**Problem:** Bypasses React's subscription mechanism. State reads are not
reactive. Mutations don't trigger re-renders in sibling components. Makes data
flow impossible to trace.

**Guideline:** Event handlers that need to write to stores should call store
actions through a dedicated event-handling service (a class or module) that
receives store dispatch functions at setup time, not by reaching into stores
globally. For one-off reads in callbacks, `getState()` is acceptable but
should be rare and documented.

---

### 2.2 Monolithic `groupMessages()` — 330 lines of imperative transformation

**Prototype:** `chatStore.ts:77-410` — A single function that rebuilds the
entire display tree from a flat message array on every render. It tracks:
tool correlations (via composite keys), plan step containers, thought
collapsing, pending action extraction, and out-of-order result buffering.

**Problem:** O(n) recomputation on every message addition. Mixes 6+ concerns.
Impossible to test individual behaviors in isolation. Mutable Maps used as
accumulator state make the logic fragile.

**Guideline:**

- **Normalize the store.** Maintain tool items, plan steps, and pending actions
  as separate indexed collections (keyed by their IDs) in the store itself —
  not derived on each render from a flat list.
- **Incremental updates.** When a `tool_result` arrives, update the existing
  tool entry directly instead of re-scanning the entire message list.
- **Separate concerns.** Thought collapsing, pending action extraction, and
  plan step nesting should be separate selectors or derived slices, not a
  single imperative pass.
- **Test each transformation** with unit tests against its own interface.

---

### 2.3 Dual bookkeeping: chatStore + panelStore for plan data

**Prototype:** Plan events are processed in both `useSessionEvents.ts` (pushed
to `panelStore.addPlanGroup` AND `chatStore.addMessage`) and then re-processed
in `chatStore.groupMessages()` (which rebuilds a `stepIndexMap`). History
reload also calls both `setMessages` and `panelStore.rebuildFromEvents`.

**Problem:** Two parallel state trees for the same data. If one is updated
and the other isn't, they diverge. The `rebuildFromEvents` function in
panelStore (146-219) duplicates groupMessages' plan parsing logic.

**Guideline:** Plan data has a single source of truth. Either:

- panelStore owns plan state and chat components read from it, or
- chatStore owns everything and panelStore is removed.

Avoid ever storing the same data in two places with independent update paths.

---

### 2.4 Ad-hoc `resolved` flag as state machine

**Prototype:** `resolveAction()` in `chatStore.ts:527-538` sets
`metadata.resolved = true` on messages. `groupMessages()` and
`extractPendingActions()` check `metadata?.resolved === true` to filter.
Components also maintain local `resolved` state (`ToolConfirmation.tsx:37`,
`AskUserPanel.tsx:26`, `StepLimitPrompt.tsx:20`).

**Problem:** Dual state tracking (store metadata flag + local component state).
No explicit state machine. If the component is unmounted and remounted, the
local `resolved` state is lost but the store flag persists — or vice versa
on history reload.

**Guideline:** Model action lifecycle as an explicit state machine in the store:
`pending → in_progress → resolved(outcome)`. Components should read this state,
not maintain a parallel copy. Use discriminated unions for clarity.

---

### 2.5 Manual `persistState()` calls in fileViewerStore

**Prototype:** `fileViewerStore.ts` — Every mutation (`openFile`, `closeFile`,
`setActiveFile`, `closeAllFiles`, `setPanelWidth`, `toggleCollapsed`) manually
calls `persistState(get())` at the end.

**Problem:** Easy to forget a call. Persistence logic is spread across every
action. If a new action is added, persistence won't happen unless the developer
remembers to add the call.

**Guideline:** Use Zustand's `persist` middleware:

```ts
create(persist<FileViewerState>((set, get) => ({ ... }), {
  name: 'c0wrk-file-viewer',
  partialize: (state) => ({ /* only persist what's needed */ }),
}))
```

This makes persistence automatic, declarative, and impossible to forget.

---

### 2.6 Module-level side effects in store files

**Prototype:** `fileViewerStore.ts:282-306` — After the store is created,
persisted state is loaded and files are opened at module import time (outside
any React component).

**Problem:** Runs before React mounts. Races with Wails runtime availability
(the `window.go.desktop.App.ReadFile` calls may fail). No error boundary
catches failures. Can't be controlled or retried.

**Guideline:** Initialization from persisted state should be triggered
explicitly from a React component (e.g. an `<AppInitializer>` or `useEffect`
in `App.tsx`) after confirming runtime readiness. Never trigger async side
effects at module import time.

---

### 2.7 Unstable selectors cause infinite re-render (React 19 + Zustand 5)

**Discovered:** During debugging of React error #185 ("Maximum update depth
exceeded") at application startup. Root cause traced to `selectSessionMessages`
in `chatStore.ts`.

**Problem code:**

```ts
// ❌ BAD — creates a new array on EVERY selector invocation
export function selectSessionMessages(
  state: ChatState,
  sessionId: string,
): ChatMessageUI[] {
  const order = state.messageOrder[sessionId] ?? [];
  return order.map((id) => state.messages[sessionId]?.[id]).filter(Boolean);
}

// ❌ BAD — used inside a Zustand selector
const messages = useChatStore(
  (s) => (activeSessionId ? selectSessionMessages(s, activeSessionId) : []),
  //                                                            ^^ new [] ref every render too
);
```

**Mechanism:** Zustand 5 uses React 19's `useSyncExternalStore`. On each
render, React calls the selector to get the current "snapshot." If the snapshot
is not referentially equal (`===`) to the previous one, React schedules a
re-render. A selector that returns a new array/object triggers a re-render,
which calls the selector again, which returns yet another new reference —
infinite loop.

This also applies to:

- `.map()`, `.filter()`, `.slice()` inside selectors
- `[]` or `{}` as fallback values (each creates a new reference)
- Spread operators: `{ ...s.config, extra: computed }`
- Any derived computation that allocates

**Fix pattern — granular selectors + useMemo:**

```ts
// ✅ GOOD — stable module-level constant for empty state
const EMPTY_MESSAGES: ChatMessageUI[] = [];

// ✅ GOOD — custom hook with granular selectors
export function useSessionMessages(sessionId: string | null): ChatMessageUI[] {
  // Each selector returns a direct store reference (stable between renders
  // as long as the underlying data hasn't changed)
  const messageOrder = useChatStore((s) =>
    sessionId ? s.messageOrder[sessionId] : undefined,
  );
  const messageIndex = useChatStore((s) =>
    sessionId ? s.messages[sessionId] : undefined,
  );

  // Derive the array only when dependencies change
  return useMemo(() => {
    if (!messageOrder || messageIndex == null) return EMPTY_MESSAGES;
    const result: ChatMessageUI[] = [];
    for (const id of messageOrder) {
      const msg = messageIndex[id];
      if (msg) result.push(msg);
    }
    return result;
  }, [messageOrder, messageIndex]);
}
```

**Rules:**

1. **Selectors must return referentially stable values.** Primitives (string,
   number, boolean, null, undefined) are always safe. Direct store property
   references (`s.foo`) are safe if the store uses immutable updates.
2. **Never allocate inside a selector.** No `.map()`, `.filter()`, spread,
   `Object.keys()`, or literal `[]`/`{}`.
3. **Use module-level constants for empty/default values.** Replace `[]` with
   `EMPTY_ARRAY`, `{}` with `EMPTY_OBJECT`.
4. **Derive in a custom hook.** Use granular selectors for individual store
   references, combine with `useMemo`. The hook replaces the selector.
5. **Zustand's `shallow` comparator is NOT sufficient** for dynamically
   allocated arrays — it prevents re-renders when elements are equal, but
   `useSyncExternalStore` runs the selector on every React render cycle, not
   just on store updates, so shallow comparison adds overhead without solving
   the root issue.

**Reference:** [pmndrs/zustand#1936](https://github.com/pmndrs/zustand/discussions/1936)

**Affected files (fixed):**

- `stores/chatStore.ts` — added `EMPTY_MESSAGES` + `useSessionMessages` hook
- `components/chat/ChatArea.tsx` — replaced selector with hook
- `components/chat/PendingActionsBar.tsx` — replaced selector with hook

---

## 3. Event System

### 3.1 25+ event subscriptions in a single `useEffect`

**Prototype:** `useSessionEvents.ts` — 704 lines. One `useEffect` subscribes
to ~25 session-scoped events. Each handler does type validation, message
construction (with ad-hoc ID generation), and store mutation.

**Guideline:**

- Split into focused handlers: `usePlanEvents`, `useToolEvents`,
  `useChatEvents`, `useLifecycleEvents`, etc.
- Define an `EventHandler` registry pattern: `{ eventType: handlerFn }` map
  that the hook iterates to subscribe.
- Each handler function is independently testable.

---

### 3.2 String-concatenated event names with no type safety

**Prototype:** `useSessionEvents.ts:126-127`

```ts
const on = (type: string, cb: (...data: unknown[]) => void) => {
  unsubs.push(runtime.EventsOn(`session:${sessionId}:${type}`, cb));
};
```

**Problem:** Event names are untyped strings. Typos are silent failures.
No autocomplete. No way to discover all events.

**Guideline:** Define a typed event map:

```ts
type SessionEventMap = {
  routing: RoutingData;
  tool_call: ToolCallData;
  tool_result: ToolResultData;
  // ...
};
```

Provide a typed `on` helper that only accepts keys from this map and infers
the callback parameter type. This gives compile-time safety.

---

### 3.3 `Date.now()` as message IDs

**Prototype:** Throughout `useSessionEvents.ts`:
`routing-${Date.now()}`, `tool-${toolCall.step}-${callIdx}`,
`assistant-${Date.now()}`, `error-${Date.now()}`, etc.

**Problem:** `Date.now()` can collide (two events in the same millisecond).
The composite key format varies by message type. Tool call/result correlation
depends on reconstructing these composite keys in `groupMessages()`.

**Guideline:** Use `crypto.randomUUID()` or a monotonic counter for frontend-
generated IDs. For tool correlation, use the backend-provided `tool_call_id`
exclusively. Define a single `generateMessageId()` utility.

---

### 3.4 Stale response protection via manual ref counters

**Prototype:** `Sidebar.tsx:101-106,181-192` — Increments a ref on each
fetch, checks if still current after the async call returns.

```ts
const myId = ++projectFetchIdRef.current;
// ... async call ...
if (myId !== projectFetchIdRef.current) return;
```

Duplicated for both project and session fetching.

**Guideline:** Use `AbortController` with the native `signal` parameter
where possible. For Wails RPC calls that don't support abort, create a reusable
`useLatestAsync` hook that encapsulates the ref-counter pattern once:

```ts
function useLatestAsync() {
  const ref = useRef(0);
  return (fn: () => Promise<void>) => {
    const id = ++ref.current;
    return fn().then(() => {
      if (id !== ref.current) throw new StaleError();
    });
  };
}
```

---

### 3.5 Triple-redundant project loading

**Prototype:** `Sidebar.tsx` has three overlapping mechanisms for loading
projects:

1. `loadProjects()` called on mount (line 156)
2. `projects:loaded` event listener (line 127)
3. `backend:ready` event listener (line 163) — which may pre-emit data OR
   trigger another `loadProjects()` call

**Problem:** Redundant fetches, race conditions between them, duplicated
"auto-select first project" logic in each path.

**Guideline:** Define a single, clear readiness protocol:

1. Frontend subscribes to `backend:ready` (the authoritative signal).
2. `backend:ready` payload always includes the initial data (projects, sessions).
3. Frontend initializes from this single payload.
4. No speculative fetches before readiness.

---

### 3.6 `window?.runtime` guard scattered everywhere

**Prototype:** At least 8 components individually check
`if (!window?.runtime) return` or `const rt = window?.runtime; if (!rt)`.

**Guideline:** The `useWails` hook should be the single entry point. It should
expose an `isReady` boolean and a guaranteed-non-null `runtime` when ready.
Components should conditionally render (or show a loading state) based on
`isReady`, not guard each event subscription individually.

---

## 4. Component Design

### 4.1 God component: Sidebar (626 lines)

**Prototype:** `Sidebar.tsx` handles project CRUD, session CRUD, search
filtering, 4 event subscriptions, inline rename for both entities, dropdown
rendering, settings modal trigger, and sidebar collapse.

**Guideline:** Split into:

- `ProjectSelector` — dropdown + CRUD + rename
- `SessionSelector` — dropdown + search + CRUD + rename
- `SidebarHeader` — layout wrapper for the two selectors + action buttons
- `useProjectLoader` hook — project fetching + backend event subscriptions
- `useSessionLoader` hook — session fetching on project change
- `Sidebar` — composition shell

Each should be <100 lines.

---

### 4.2 MemoryBlock and ToolBlock are near-duplicates

**Prototype:** `MemoryBlock` (defined inside `ChatMessageRenderer.tsx:41-154`)
and `ToolBlock` (`ToolBlock.tsx`) share ~90% of code: argument parsing,
`formatValue`, result length formatting, `MAX_PREVIEW`, show/hide toggle,
collapsible layout, status icon logic.

**Guideline:** Extract a shared `CollapsibleToolCard` component that accepts
`icon`, `label`, `statusIcon`, `args`, `result`, `resultLen`, and render
configuration. `ToolBlock` and `MemoryBlock` become thin wrappers that provide
their specific icon, label, and status mapping.

---

### 4.3 Type guards duplicated across files

**Prototype:**

- `Sidebar.tsx:31-45`: `isProjectInfo`, `isSessionInfo`, etc.
- `useSessionEvents.ts:11-93`: 20+ type guard functions
- `App.tsx:15-19`: `isStartupError`
- `wails.ts:172-184`: `isContextCompactionData`, `isSessionTokensData`

**Guideline:** Centralize all event data type guards in a single module
(e.g. `lib/typeGuards.ts`). Better: use a schema validation library (Zod)
to generate both types and validators from a single source.

---

### 4.4 `PlanStepBlock` uses setState-during-render

**Prototype:** `PlanStepBlock.tsx:49-57` — Compares current status with
previous status during render and calls `setIsOpen()` / `setPrevStatus()`.

**Problem:** This is the legacy getDerivedStateFromProps pattern, which is
confusing and triggers an extra synchronous re-render.

**Guideline:** Derive the open state from status directly:

```ts
const isAutoOpen = status === "running";
const [userOverride, setUserOverride] = useState<boolean | null>(null);
const isOpen = userOverride ?? isAutoOpen;
```

Reset `userOverride` to `null` when status changes via a `useEffect`.

---

### 4.5 Large switch statement for rendering display items

**Prototype:** `ChatMessageRenderer.tsx:157-210` — 53-line switch that maps
16 `DisplayItem.kind` values to components.

**Guideline:** Use a component registry:

```ts
const renderers: Record<DisplayItem['kind'], ComponentType<...>> = {
  user: UserMessage,
  assistant: AssistantMessage,
  // ...
}
```

The render function becomes:

```ts
const Comp = renderers[item.kind]
return Comp ? <Comp {...item} /> : null
```

---

### 4.6 Nested dropdowns (dropdown-inside-dropdown)

**Prototype:** `Sidebar.tsx:359-386,416-447` — `<DropdownMenu>` inside
`<DropdownMenuItem>` for session/project context actions.

**Problem:** Nested Radix popovers have focus trapping issues. Users must
`e.stopPropagation()` on every click. UX is confusing (hover opens outer,
click opens inner).

**Guideline:** Use a different pattern for item actions:

- Right-click context menu (`ContextMenu` from Radix), or
- Action buttons that appear on hover (like VS Code's tree items), or
- A single-level dropdown with grouped sections.

---

### 4.7 `formatRelativeTime` defined in Sidebar

**Prototype:** `Sidebar.tsx:47-60` — A general-purpose time formatting
function defined in a component file.

**Guideline:** Move to `lib/formatters.ts` alongside the existing
`formatDuration`. All formatting functions in one place.

---

## 5. Data Flow & Architecture

### 5.1 No API abstraction layer

**Prototype:** Components call Wails bindings in three different ways:

1. `useWails().api.CreateSession()` (ChatInput)
2. `import { GetSessionHistory } from '../../wailsjs/go/desktop/App'` (ChatArea)
3. `import { ResumeTask } from '../../../wailsjs/go/desktop/App'` (ResumeActionPanel)

**Problem:** No single control point for RPC calls. No centralized error
handling, logging, or retry logic. Import paths vary by file depth.

**Guideline:** Create an `api/` module that re-exports all RPC methods with:

- Typed signatures (already have these in `useWails.ts` `Window` declaration)
- Centralized error handling (log + throw typed errors)
- Runtime-ready guard (throws if called before Wails is ready)

All components import from `@/api/*`, never from `wailsjs/go/desktop/App`.

---

### 5.2 ReactMarkdown plugin list duplicated

**Prototype:** `AssistantMessage.tsx:67-74` and `FileViewerContent.tsx:143-149`
both construct identical plugin arrays.

**Guideline:** `markdownConfig.tsx` should export the complete plugin arrays
(remark + rehype) as named constants, not just the schema and components.
A single `<Markdown content={...} />` wrapper component can encapsulate the
full pipeline.

---

### 5.3 `splitHighlightedLines` — hand-written HTML parser

**Prototype:** `FileViewerContent.tsx:207-268` — Character-by-character
walk through highlight.js HTML to split by newlines while preserving tag
balance.

**Problem:** Fragile. Doesn't handle all HTML edge cases (self-closing tags
with attributes, entities, etc.). Complex and hard to test.

**Guideline:** Two cleaner approaches:

1. **Highlight per-line:** Split the source into lines first, highlight each
   line individually. Loses cross-line token context but is simple and robust.
2. **DOM-based splitting:** Render the highlighted HTML into a hidden DOM node,
   walk the DOM tree, and extract line-level fragments. More correct, still
   simpler than manual parsing.

---

### 5.4 Width sync ping-pong in AppLayout

**Prototype:** `AppLayout.tsx:42-49` — Two `useEffect`s sync width between
`useResizeHandle` (local state) and `fileViewerStore` (persisted state).
One has `// eslint-disable-line react-hooks/exhaustive-deps`.

**Problem:** Bidirectional sync effects can oscillate. The eslint suppression
hides a real dependency issue.

**Guideline:** The resize hook should accept an `initialWidth` and an
`onChange` callback. The store owns the persisted width. The hook reads
initial from store and reports changes back. No bidirectional sync needed.

---

### 5.5 ResizeHandle has split logic

**Prototype:** `ResizeHandle.tsx` accepts both `onMouseDown` (for drag) and
`onResize` (for keyboard). But in `AppLayout.tsx:86`, the `onResize` callback
does its own clamping (`Math.max(MIN, Math.min(MAX, w + delta))`) which
duplicates what `useResizeHandle` already does internally.

**Guideline:** The resize hook should own all resize logic (drag + keyboard).
The component should be a thin visual element. Clamping lives in one place.

---

## 6. Miscellaneous

### 6.1 `mounted` flag anti-pattern

**Prototype:** `useSessionEvents.ts:100` — `let mounted = true` with
`mounted = false` in cleanup.

**Guideline:** Use `AbortController.signal.aborted` as the canonical
"unmounted" check. Pass the signal through the event subscription lifecycle.
This is the modern React pattern that also supports cancellation.

---

### 6.2 Settings modal positioned with magic number

**Prototype:** `SettingsModal.tsx:42` — `top-[40px] translate-y-0`

**Guideline:** Position via the Dialog component's built-in alignment options
or a Tailwind class that references a named spacing token, not a hardcoded
pixel value.

---

### 6.3 Dead buttons in About page

**Prototype:** `SettingsModal.tsx:129-147` — "Documentation", "Report an
Issue", "GitHub Repository" buttons with no `onClick` handlers.

**Guideline:** Remove placeholder UI that does nothing. Add functionality
or don't render the element. Dead UI erodes trust.

---

### 6.4 Hardcoded version string

**Prototype:** `SettingsModal.tsx:114` — `Version 0.0.1` hardcoded in JSX.

**Guideline:** Import version from a build-time constant (Vite's
`import.meta.env` or a generated `version.ts`). The backend already has the
version; expose it via an RPC method or build-time injection.

---

### 6.5 ResizeObserver + rAF fallback over-engineering

**Prototype:** `ChatArea.tsx:35-69` — `useLayoutEffect` that does immediate
measurement, rAF fallback measurement, and ResizeObserver setup with feature
detection.

**Problem:** Three measurement strategies for one value suggest the layout
was unreliable. This is treating symptoms, not causes.

**Guideline:** Rely solely on ResizeObserver (supported in all modern
browsers). If the initial measurement is 0, it means the element isn't
mounted yet — ResizeObserver will fire when it is. No rAF fallback needed.

---

### 6.6 `isRecord` / type guards for `metadata` everywhere

**Prototype:** Every component that reads message metadata casts it:

```ts
const meta =
  typeof msg.metadata === "object" && msg.metadata !== null
    ? (msg.metadata as Record<string, unknown>)
    : undefined;
```

Then extracts fields with individual type checks.

**Guideline:** Define typed metadata interfaces for each message type.
Parse metadata once at the event ingestion boundary (in the event handler).
Store typed objects in the store. Components receive strongly-typed props,
never `Record<string, unknown>`.

---

## Summary: Key Principles for Clean Implementation

1. **Design tokens are law.** Every color, spacing, and radius references a
   CSS custom property. No raw hex in component code.

2. **No `!important`.** If you need `!important`, the abstraction is wrong.
   Fix the component API, not the CSS specificity.

3. **Single source of truth.** Each piece of data lives in exactly one store.
   No dual bookkeeping, no parallel state trees.

4. **Normalized store, incremental updates.** Don't rebuild trees from flat
   arrays on every render. Index by ID, update in place.

5. **Type safety at boundaries.** Validate and type event data at the ingestion
   point. Everything downstream is typed — no `Record<string, unknown>`.

6. **Small, focused components.** No file over 200 lines. No component that
   handles more than one domain concept. Extract hooks for data loading.

7. **One import path for backend calls.** All RPC goes through `@/api/*`.
   No direct imports from `wailsjs/go/desktop/App`.

8. **Declarative persistence.** Use Zustand middleware, not manual
   `localStorage` calls.

9. **No module-level side effects.** Store files define stores. Initialization
   happens in React lifecycle hooks after runtime readiness is confirmed.

10. **Event handlers are testable.** Each event type has a focused handler
    function that can be unit-tested in isolation without React rendering.

11. **Stable Zustand selectors.** Every selector passed to a Zustand hook must
    return a referentially stable value. Never allocate arrays or objects inside
    a selector — use granular selectors + `useMemo` in a custom hook instead.
    Violation causes infinite re-render loops in React 19 (error #185).
