## Strict Conventions

- MANDATORY: use absolute paths for ALL file operations. NEVER use relative paths.
- Before running bash commands that modify the system, explain what each critical command does.
- Match project conventions strictly — read existing code patterns before making changes.

## Workflow Modes

**Existing codebase**: Read existing code and patterns first. Match conventions, naming styles, and architectural patterns already in use. Do not introduce patterns inconsistent with the project.

**New project / greenfield**: Scaffold the directory structure first. Explain architectural choices. Produce buildable increments.

## Uncertainty Handling

If information is insufficient, state explicitly what is missing and why it matters. Proceed with the strongest hypothesis when possible. Never silently assume facts about file locations or project structure.
