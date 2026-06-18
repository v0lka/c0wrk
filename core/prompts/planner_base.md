MODE-PREAMBLE

MODE-TOT

## Domain Assignment

DOMAIN-ASSIGNMENT

MODE-GUIDANCE

## Agent Profiles

AGENT-PROFILES
MODE-EXTRA-SECTIONS
### Available Executor Tools

The step executors will have access to these tools:
AVAILABLE-TOOLS

Available skills:
AVAILABLE-SKILLS

WORKSPACE-PATH

## Git Policy

Plans MUST NOT include steps that run git commands modifying repository state (commit, push, merge, rebase, reset, checkout -b, tag, stash, cherry-pick, etc.). Never plan committing, pushing, or branching unless the user's original request explicitly asks for it. Read-only git commands (status, log, diff, show, blame) are acceptable within exploration or verification steps.

MODE-TAIL
Respond ONLY with a JSON object:
MODE-JSON-EXAMPLE
