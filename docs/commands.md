# Custom Commands

Custom commands are markdown prompt templates you invoke with a leading
`/name` token in the chat input — the fourth `/` category alongside
subagents, MCP servers, and skills. Unlike a mention (which annotates a
message), a command **replaces** the message with the expanded template
before it is sent. See [ADR-096](../specs/decisions/096-custom-commands.md)
for the design decision.

## Create a command

Create a markdown file in a command directory:

- **Project**: `<workspace>/.agents/commands/<name>.md` — highest priority,
  shared with the repo
- **c0wrk global**: `~/.c0wrk/.agents/commands/<name>.md` — your machine, all
  projects
- **User**: `~/.agents/commands/<name>.md` — user-wide (also read by other
  tools)

The file name becomes the command name: `deploy.md` → `/deploy`. Names may
contain letters, digits, `_`, and `-`.

```markdown
---
description: Run tests with coverage
agent: code-reviewer
model: gpt-x
---
Run the full test suite with a coverage report and summarize any failures.
Focus on the failing tests and suggest fixes.
```

The frontmatter is optional; the body is the template.

| Field         | Meaning                                                                                                  |
| ------------- | -------------------------------------------------------------------------------------------------------- |
| `description` | Shown in the `/` completion list                                                                          |
| `agent`       | A Subagent Profile name requested for this run (like typing `/agent: name` yourself)                      |
| `model`       | A fallback model override — your explicit toolbar selection always wins                                   |

The first file claiming a name wins (project → c0wrk global → user).

## Invoke a command

Type `/` and pick from the **Commands** section (the first one), or type the
name directly. Everything after the command token becomes its arguments:

```
/deploy prod --fast
```

### Placeholders

- `$ARGUMENTS` — the raw argument remainder (`prod --fast` above)
- `$1`, `$2`, … — positional arguments; whitespace-separated, a
  double-quoted run counts as one argument:

```
/create-file config.json src "{ \"key\": \"value\" }"
```

- Missing positionals expand to the empty string.
- Substitution is a single pass: arguments are inserted literally and never
  re-expand.

### Composability

A template may use the ordinary message machinery:

- `/skill-name` or `/agent: name` mentions inside a template activate
  normally for that run.
- `@file` references resolve like any typed message.

## Scoping rules

- Only a token at the **start** of the message invokes a command; a
  mid-message `/name` stays a mention.
- If the name exists in more than one catalog (say a skill and a command are
  both called `test`), a plain `/test` is an ambiguous no-op with a hint —
  use the qualified form `/command: test` to invoke the command.
- `/command:` is always valid, even without a collision. It accepts any
  number of spaces or tabs after the colon (`/command:   test` still invokes
  `test`) — a deliberate widening so extra spacing in hand-typed input cannot
  degrade an invocation; mentions (`/agent:`, `/skill:`, `/mcp:`) still
  accept at most one space after the colon.
- A command named `goal` shadows the built-in `/goal` (the same happens with
  an agent, a skill, or an MCP server named `goal` — the plain spelling
  resolves to the catalog entry and the built-in disappears from the
  completion list) — rename it if you still need the plain spelling. The
  budgeted `/goal:N` form is never a catalog mention, so it still arms goal
  mode with an N-turn budget
  ([ADR-106](../specs/decisions/106-goal-mode-chat-activation.md)).

## v1 limitations

- No shell injection: opencode's `` !`command` `` syntax is not supported and
  stays literal text (it needs its own hardened execution path — a future
  change).
- No escape syntax: a literal `$1` in a template is indistinguishable from
  the placeholder.
- No `subtask` option: delegation belongs to the Conductor.
- Command directories configured (or created) after startup are picked up on
  the next app restart, same as skill/agent directories.
