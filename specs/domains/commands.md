# Custom Commands (ADR-096)

## Role

Defines the file-backed custom command system: markdown prompt templates
invoked with a leading `/name` token in the chat input — the fourth `/`
category alongside the three mention catalogs (Subagents, MCP servers,
Skills). Commands are INVOCATIONS that replace the message with an expanded
template; they are not mentions, do not touch the orchestrator, and have no
capability activation modes. Decision: [ADR-096](../decisions/096-custom-commands.md).

## Key Files

- `backend/config/config.go` — `CommandsConfig` (`commands.dirs`)
- `backend/config/defaults.go` — `defaultCommandDirs` (`~/.c0wrk/.agents/commands`, `~/.agents/commands`; nil → defaults, empty → opt-out)
- `backend/config/paths.go` — `ProjectCommandsPath` (`<workspace>/.agents/commands`; the single owner of the path literal)
- `backend/frontend_api_commands.go` — discovery scan, frontmatter parser, cache, global-dir watchers, `ListCommands` RPC
- `backend/events.go` — `EventCommandsChanged` (`commands:changed`)
- `backend/api_types.go` — `CommandDescriptorDTO` (name, description, agent, model, template)
- `frontend/src/api/commands.ts` — `listCommands()` wrapper + boundary guard (degrades to `[]`)
- `frontend/src/lib/commandsExpand.ts` — pure leading-token parser, four-catalog resolution, template expansion
- `frontend/src/hooks/useChatInputController.ts` — send-path invocation (resolve → expand → re-extract refs → send)
- `frontend/src/hooks/useMessageSender.ts` — `SendOptions.commandModel` (the fallback model override)
- `frontend/src/lib/cmChatAutocomplete.ts` — the `Commands` completion section (rank 0)

## Behavior

### File format

```
.agents/commands/deploy.md
---
description: Run tests with coverage
agent: code-reviewer        # optional — an extra requested subagent
model: gpt-x                # optional — a FALLBACK model override
---
Run the full test suite for $ARGUMENTS and summarize failures.
```

- The file name sans `.md` is the command name; only `[\w-]+` names are
  discoverable (the mention character class).
- Unknown frontmatter keys are ignored; an unparsable frontmatter is
  fail-soft (warning + empty metadata; the body still ships). A missing
  description renders the generic completion detail.
- Placeholders: `$ARGUMENTS` = the raw remainder after the token; `$N` = the
  Nth positional (whitespace-split; a double-quoted run groups as one
  argument; `\"`/`\\` escapes inside quotes). One pass, substituted values
  are literal, missing positionals expand to `''`. No escape syntax in v1.

### Discovery (three levels, highest first)

1. Project-local `<workspace>/.agents/commands/` — always scanned (a
   No-Project session has none).
2. `commands.dirs` config entries, resolved like `skills.dirs` (~, env vars,
   agent-dir-relative); defaults put the c0wrk global dir above the user's.
3. First file claiming a name wins; every returned name is unique.

The resolved dir list is fixed at startup (the watchers' trade-off). Watchers
on the global dirs emit `commands:changed`; workspace-local changes surface
via `workspace:tree_changed`; project/session switches invalidate the
frontend cache.

### Invocation (leading token only)

- Plain `/name args…` — resolves against FOUR catalogs (agents, skills, MCP
  servers — session-kind-filtered as usual — plus commands): in ≥2 → the
  ADR-076 ambiguous no-op (hint names `/command:` fourth); exactly commands →
  expand; exactly one mention catalog → the mention pipeline; none → the
  permissive skill passthrough. The one carve-out: a plain `goal` token
  naming no catalog entry is the built-in `/goal` command
  ([ADR-110](../decisions/110-goal-mode-chat-activation.md)), never a
  passthrough mention — it rides the text so the backend detector sees it.
- Qualified `/command: name args…` (both colon spellings; ANY run of spaces
  or tabs after the colon — a deliberate divergence from the ADR-076 mention
  grammar, which keeps its single optional space for `/agent:`/`/skill:`/
  `/mcp:` and is unchanged) — always valid; an unknown id, or a colon
  followed by no name at all, is preserved verbatim as plain text.
- The expanded text REPLACES the message and flows through the ordinary send
  path: mention refs inside the template are re-extracted and partitioned
  normally, `@file` references resolve via the existing fileref pipeline, and
  a template starting with `/goal` — or `/goal:N`, which carries the N-turn
  budget through the expansion ([ADR-110](../decisions/110-goal-mode-chat-activation.md)) —
  triggers goal mode (an E2S send then rejects it server-side, as always).
- Frontmatter bindings: `agent` merges into the partitioned agents
  (deduplicated); `model` rides as `SendOptions.commandModel` — used only
  when the toolbar has no explicit selection.
- Empty expansion (whitespace template) → no-op + hint naming the empty
  template. A failed send restores the ORIGINAL invocation text.
- A command named `goal` shadows the built-in `/goal` for the plain spelling
  (the accepted ADR-076 shadowing class — an agent, a skill, or a
  kind-visible MCP server named `goal` hides the built-in from the completion
  list the same way); `/command: goal` still invokes the file, and the
  budgeted `/goal:N` form is never a catalog mention (the `:` breaks the
  plain-token boundary), so it always reaches the built-in detector
  ([ADR-110](../decisions/110-goal-mode-chat-activation.md)).

### Completion

`Commands` section at rank 0 (Subagents 1, MCP Servers 2, Skills 3). A
command colliding with a visible mention-catalog entry is offered — and every
other section's colliding entry is re-qualified — against the union of the
four catalogs. No session-kind filter (no capability modes; project-local
commands simply do not exist without a project).

## Error Handling

- Discovery is fail-soft per file: unreadable files drop, unparsable
  frontmatter warns and keeps the command with empty metadata, uninvocable
  names are skipped (Debug).
- `listCommands()` degrades to `[]` on a malformed wire shape (boundary
  guard) and on an RPC rejection (the completion cache); the send path then
  resolves the leading token with an EMPTY command catalog — an invocation
  typed during an outage degrades to the permissive passthrough, never to a
  lost message (a `/goal` invocation instead degrades to the built-in goal
  command: it never depended on the command catalog).
- Ambiguity and empty-expansion no-ops surface a dismissible send-error hint
  and restore the typed text.

## Invariants

- Commands are invocations, never mentions: the backend mention scanner,
  `SendMessage`'s positional contract, and `HandleOptions` are untouched; a
  command can only be triggered by a leading token the USER typed or picked.
- A leading plain `/name` present in more than one catalog is a visible
  no-op — silent preference between expansion and activation is rejected.
- Substitution is one-pass and literal: template text can never re-expand
  substituted arguments.
- Command discovery paths are constructed only through
  `config.ProjectCommandsPath` (the single literal owner).
- The frontend consumes commands only through `@/api/commands` with the
  boundary guard; every catalog cache invalidates on the same transitions as
  the sibling catalogs plus `commands:changed`.

## Security Considerations

Command templates are UNTRUSTED CONTENT (the ADR-020 framing): the
project-local root `<workspace>/.agents/commands/` sits inside the workspace
and is agent-writable (the `local_write` group), so a model can plant a
command file whose template fires as a USER message the next time the
operator invokes `/name` — a durable, session-surviving injection channel in
the same accepted-risk class as the skills/AGENTS.md catalogs (an ASI01-class
persistence vector). The posture is ADR-020's: record the trade-off
explicitly rather than pretend the content is trusted. What bounds the
vector:

- **Invocation is user-typed or user-picked only.** A leading token the user
  typed, or a completion entry the user accepted, is the only trigger; the
  model cannot hallucinate or activate a command, and the backend mention
  pipeline / `SendMessage` contract never see one.
- **The expansion is transparent.** The expanded template REPLACES the
  message and is persisted in the transcript as the user turn — the model
  receives exactly the text the operator can read, with no hidden channel
  (one-pass literal substitution; no shell injection in v1).
- **No new execution capability.** A template is prompt text, not code;
  frontmatter `agent`/`model` ride the ordinary consent surfaces (a requested
  subagent the user could have typed; a model fallback the explicit toolbar
  selection always overrides).

## Related Specs

- [decisions/096-custom-commands.md](../decisions/096-custom-commands.md) — the decision
- [decisions/076-unified-slash-mentions.md](../decisions/076-unified-slash-mentions.md) — the `/` grammar and collision rule this extends
- [capabilities.md](capabilities.md) — the activation-mode system deliberately NOT extended to commands in v1
- [contracts/event-catalog.md](../contracts/event-catalog.md) (`commands:changed`), [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) (`ListCommands`)
