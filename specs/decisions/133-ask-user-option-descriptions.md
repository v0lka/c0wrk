# ADR-133: ask_user options carry an optional display description; option-level recommended stays rejected with a question-level pointer

## Status

Accepted

## Context

The `ask_user` option schema is a closed property set: sp4rk's pre-dispatch input validation (`sdktools.ValidateToolInput`) rejects any undeclared key inside `questions[].options[]` for built-in tools, and every dispatch path funnels through it — the ToolRegistry's structural gate (Gate 1, ASI02-R2), the E2S loop's pre-dispatch check, and `ExecuteUnattended`. The validator's error is deliberately generic: it names the offending key, its path, and the declared parameter set, but it cannot know any tool-specific remediation.

Models nevertheless routinely attach a `description` to each option — a natural way to give the user the one-line context ("REST: simpler tooling, wide adoption") that helps them choose. Because the schema closed that door, every such call failed pre-dispatch (~12 rejected calls/week in the observed error class) and the model paid a full round-trip to learn what the catalog could have told it up front; the user lost the hint the model wanted to give. The same genericity affects the neighboring mistake: an option-level `recommended` key is rejected with the same unknown-parameter message, which locates the error but never says the fix — `recommended` belongs on the question, next to `multi_select`.

Prose guidance alone is not a reliable surface: the Model Profiles Lite prompt swap and per-family prompt drift mean the only surfaces every model sees in every configuration are the tool description and the JSON schema (the ADR-132 reasoning, applied to `ask_user`).

## Decision

**1. Options accept an optional `description` string — tolerant ingestion.** The option schema declares `description` (string, optional); `core/tools.AskUserOption` carries `Description`; the wire shape (`PendingAskUser` → the ask-user event payload) forwards it verbatim, and `AskUserPanel` renders it as a muted one-line hint under the option label. It is display-only: it never enters the answer payload, the tool result, or the model's view of the user's selection. Every other undeclared option key is still rejected — the closed set is otherwise unchanged.

**2. Option-level `recommended` stays rejected — but the error now names the fix.** The key is *declared* in the option schema (type-free, description: "Not accepted inside an option — always rejected; put recommended at the question level") solely so the generic closed-set rejection — which cannot carry a per-tool remediation — does not preempt the tool's own validation. `AskUserTool.Execute` checks key presence and returns the actionable validation error: *"recommended is not accepted inside an option; put recommended at the question level"*. Because every dispatch path reaches `Execute`, the remediation-bearing error is uniform across the registry, E2S actions, and unattended execution. The question-level `recommended` (the only accepted placement) is untouched.

**3. The contract is declared on the tool surfaces.** The tool description states the option shape (`{label, value}` plus optional `description`) and keeps `recommended` in the anti-example with its question-level placement; the schema property descriptions say the same. The description stays under the 1200-byte rubric guard with its Inputs↔schema consistency checks. The schema routing in (2) is pinned by tests on both sides: Gate 1 must admit option-level `description`, must reject a truly unknown key (`title`), and must let option-level `recommended` through to `Execute`'s specific rejection.

## Consequences

Positive:

- The dominant rejected-call class drops to zero round-trips: the model expresses the hint on the first call, and the user gets contextual guidance in the form.
- The UI hint costs nothing downstream: the answer payload and tool result are unchanged, so no consumer of ask_user answers had to change.
- One remediation message everywhere: the question-level-pointer error is produced by the tool itself, so registry, E2S and unattended dispatch cannot drift into three different rejections for one mistake.

Negative / residual:

- The declared-but-rejected `recommended` key is visible in the model-facing catalog at the option level, which could invite the very mistake it exists to explain; the property description and the tool-description anti-example both say "rejected — question level", and the rejected call returns the actionable message on the first attempt instead of a blind retry.
- Two layers reject one key (schema declares, tool refuses), so a future tightening of the closed set silently reverts the error to the generic message; the schema-routing tests exist precisely to make that drift loud.
- Option `description` strings are model-authored content rendered in the desktop UI; React escapes text nodes, and the parse layer drops non-string values, but the field remains display-only by contract — any future rich rendering must keep that boundary.

## Alternatives Considered

- **Keep the option level closed; rely on the generic unknown-parameter error.** Rejected: the error locates the key but cannot name the remediation; the model retries blind or abandons the hint, and the user-visible loss (no contextual guidance) persists.
- **Enrich Gate 1 validation errors per tool** (special-case `ask_user` paths in c0wrk's registry wrapper). Rejected: it teaches a shared security gate to string-match one tool's payloads, and the E2S loop and `ExecuteUnattended` call `ValidateToolInput` directly, so the remediation would be inconsistent across dispatch paths — exactly the drift the tool-owned check avoids.
- **Accept option-level `recommended` and merge it with the question-level array.** Rejected: ambiguous precedence when both are present, two sources of truth for one semantic, and it widens the schema for a key whose correct home already exists one level up.
- **Open the option level entirely** (`additionalProperties: true` for options). Rejected: the closed property set is what catches misspelled or hallucinated parameters for built-in tools (ASI02-R2); opening one level for one tool erodes the guarantee and trades a targeted relaxation for an untargeted one.

## Related

- [ADR-132](132-plan-description-normalization.md) — the precedent for declaring behavioral contracts on the tool description + JSON schema (the only surfaces every model family sees in every prompt configuration).
- [tool-system README](../domains/tool-system/README.md) — the gate pipeline Gate 1 belongs to, and the closed-by-default key admission for built-in tools.
- [builtins.md](../domains/tool-system/builtins.md) — the tool catalog entry this contract extends.
- `core/tools/askuser.go` — the schema declaration, the tool description, and the Execute-level `recommended` rejection.
- `core/tools/askuser_types.go` — `AskUserOption.Description` and the never-consumed `Recommended` raw capture.
- `core/tools/askuser_test.go` — the schema-routing and rejection pins (description admitted, unknown key rejected, option-level `recommended` reaches the tool).
- `frontend/src/components/chat/AskUserPanel.tsx` — the muted hint rendering.
- `frontend/src/types/events.ts`, `frontend/src/types/messages.ts` — the boundary type + guard and the parse-layer passthrough (non-string descriptions dropped).
