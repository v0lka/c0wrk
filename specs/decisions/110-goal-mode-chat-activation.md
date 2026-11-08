# ADR-110: Built-in `/goal`/`/goal:N` Chat Activation

## Status

Accepted

## Context

Goal mode ([ADR-019](019-goal-mode.md)) had two explicit entry points: the chat input's goal toggle (with its `goalBudget` JSON transport) and a bare leading `/goal` prefix detected post-preprocessing by `DetectAndStripGoalMode`. Four gaps followed from that bare-prefix form:

- The textual entry carried no budget — an N-turn cap required the toggle's `goalBudget` field, so a message-scoped budget was impossible without touching the toolbar.
- The completion list advertised every other `/`-trigger (subagents, MCP servers, skills, custom commands) but not goal mode — the `/goal` spelling was undiscoverable.
- The chat's Goal badge keyed on the toggle flag only: a `/goal`-prefixed send armed the goal loop but its persisted user row carried no `goal: true` metadata, so the transcript badge disagreed with what actually ran until reload of a toggled send.
- Rival desktop agents expose `/goal`-style commands in their completion lists; c0wrk's derivation-based entry was the differentiator but invisible at the input.

## Decision

1. **A built-in two-form chat command.** `/goal` arms goal mode; `/goal:N` arms it with an N-turn budget. One anchored grammar in `core/message_preprocess.go` (`goalModePrefixRe = ^/goal(?:\s+|$|:(\d+)(?:\s+|$))`, RE2 `\s`) backs `DetectAndStripGoalMode(text) (cleaned, isGoal, budgetTurns)`, which runs on the POST-preprocessing text so a prefix exposed by stripping a mention (`/explore /goal …`) still activates. The stripped remainder is the message the derivation pass grounds the {condition, verify} pair from; a bare `/goal` yields an empty remainder and the derivation grounds from that. `N` requires a digit ≥ 1 within int64 range.
2. **Invalid forms are inert.** `/goal:0`, a non-digit or missing digit after the colon (`/goal:`, `/goal:x`), and an out-of-range N match nothing: no activation, the text ships verbatim as a plain message. Detection never half-fires.
3. **Textual N outranks the toggle budget.** The session manager merges the two per-message sources in `resolveGoalBudgetOverride` (`backend/session/manager_execution.go`): the toggle's `goalBudget` JSON is parsed fail-soft (empty/malformed → nil, logged, never blocks the send), then a positive textual N replaces it; a bare `/goal` keeps the toggle budget when set; neither source → nil (unlimited). The message-scoped signal wins because it is the more specific intent.
4. **Badge parity.** `SendMessage` stamps the persisted user row's metadata `goal: true` for ANY armed send — the flag OR a detected prefix (`goalEnabled := goal || isGoalPrefix` feeding `PendingMessageMetadata`) — so the chat Goal badge renders identically for both entry forms. The frontend mirrors the check on the RAW text (`frontend/src/lib/goalCommand.ts` `detectGoalCommand`, a byte-for-byte grammar mirror with an explicit ASCII whitespace class) for the optimistic card; a prefix exposed only by mention-stripping is stamped backend-side and its badge appears on reload — the backend detection is authoritative.
5. **Completion entry with shadowing parity.** The `/` completion list offers `goal` in the Commands section (rank 0, detail naming `/goal:N`) as a built-in; it is HIDDEN whenever a kind-visible catalog entry — an agent, a skill, a session-kind-filtered MCP server, or a user command — is named `goal`, because that plain spelling then resolves to that entry (the accepted ADR-076/ADR-096 shadowing class; the bare form has no qualified escape — only the budgeted `/goal:N` form is mention-proof and stays reachable). The goal toggle is unaffected by any shadowing.
6. **Every goal gate keys on the union.** The live-send rejection (a `/goal`/`/goal:N` message into a running task is rejected, never queued), the E2S mutual exclusion (a both-flags request AND a prefix-armed E2S send are rejected before any side effect), the Model Profiles essential-tools block, and the detection-before-resume-check ordering (a goal request abandons the interrupted task instead of resuming it) all treat a prefix-armed send exactly like a toggled one — one `goalEnabled` signal everywhere.

## Consequences

**Positive:**

- Goal mode is discoverable and message-scoped: budget travels with the text (`/goal:5 fix the flaky test`), so two goal sends in one session can carry different caps without touching the toolbar.
- The transcript badge is honest about what ran: prefix sends render the Goal badge like toggled sends, persistently (metadata) and optimistically (raw-text mirror).
- Competitor-parity entry syntax without giving up the derivation-then-confirm UX — the command arms the loop, it does not author the condition (ADR-019 Decision 2 stands).

**Negative / trade-offs:**

- A catalog entry named `goal` shadows the built-in for the plain spelling with no qualified form that reaches it — recovery is renaming the entry or using the toggle (accepted, user-caused, reversible; same class as ADR-096's shadowing trade-off).
- The frontend optimistic badge checks raw text only, so a mention-exposed `/goal` prefix shows its badge one reload late (the backend stamp is the authority); the divergence window is a single optimistic card.
- Two budget sources with a priority rule is one more merge semantic to document; malformed toggle JSON stays silent-by-design (logged, unlimited) and can only be diagnosed from logs.

## Alternatives Considered

- **Config-level budget for the textual form** (map `goal_loop.default_textual_budget`). Rejected: the budget is per-message intent; a config default cannot express `/goal:5` vs `/goal:20` in the same session and hides the cap from the message that requested it.
- **Toggle budget wins over textual N.** Rejected: the toolbar setting is a session-wide default; the message's explicit `:N` is the more specific, later-formed intent. A bare `/goal` deliberately keeps the toggle budget so both precedence directions are intentional.
- **Reserved-name protection for `goal`** (no catalog entry may take the name). Rejected for the same reasons ADR-096 rejected protecting `/goal` from custom commands: catalog names come from unenforceable, user-owned precedence roots, the collision is visible in the completion list, and the toggle remains an always-available fallback.
- **Client-side-only budget parsing** (frontend strips `/goal:N` and sends `goalBudget`). Rejected: the backend must own activation semantics (the E2S/gate/live-send checks and the persistence stamp all run server-side on post-preprocess text); a client-stripped prefix would leave the gates blind to the exposed-prefix form.

## Related Specs

This ADR refines, without superseding: [ADR-019](019-goal-mode.md) §2 (the entry command gains a budget form; the condition is still derived, never authored), [ADR-076](076-unified-slash-mentions.md) §9 (the preprocessing-survival argument covers `/goal:N` identically), [ADR-086](086-goal-mode-suggestion.md) (the suggestion gate's "no explicit goal" precondition covers both command spellings), [ADR-042](042-goal-mode-slm-essential-tools-gate.md) (the essential-tools block covers both spellings), and [ADR-096](096-custom-commands.md) (a template starting with `/goal:N` now carries the textual budget through the expansion).

- [../domains/goal-mode.md](../domains/goal-mode.md) — the goal-mode domain (Mechanism § Chat Entry, Budgets, Suggested Activation)
- [../domains/session-lifecycle.md](../domains/session-lifecycle.md) — the send path, live-send rejection, detection-before-resume-check
- [../domains/commands.md](../domains/commands.md) — custom commands and the shadowing rule
- [../domains/model-profiles.md](../domains/model-profiles.md) — the goal gate both spellings hit
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the `SendMessage` contract (`goalBudget`, textual priority, metadata stamp)
