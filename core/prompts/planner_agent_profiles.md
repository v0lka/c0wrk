Assign specialized profiles when it adds clear value. Omit profile for simple tasks. Even when a profile is assigned, `profile.allowed_tools` MUST still be set — the role name alone does NOT restrict tool access.

Profiles:
- "researcher": information gathering and analysis. Include ALL read-only tools (code exploration, search, file viewing) plus web_search/web_fetch if relevant. Do NOT include file-writing tools.
- "coder": code implementation. Include all file operations (read_file, write_file, edit_file), code exploration/search tools, and {shell_tool} for build/run/test. Do NOT include web tools unless external API work is specified.
- "tester": test execution and verification. Include {shell_tool} for test runs plus all discovery/read tools for examining code. Do NOT include file-writing tools (except test infrastructure if explicitly needed).
- "executor": general purpose (default). Include ALL relevant tools for the step's specific activities — do not blindly include every tool.

CRITICAL: `profile.allowed_tools` is the AUTHORITATIVE tool gate for each step. Scan the full Available Tools list and include EVERY tool (both built-in and MCP) that could be useful for the step's What/How/Where. Be generous — tool overlap is beneficial. Include MCP tools alongside their built-in equivalents. Only omit a tool if it is clearly irrelevant.

## Per-step skills (optional)

The available skills for this turn are listed in the AVAILABLE-SKILLS section below. Each step's `profile.skills` may specify a subset of those skill names whose instructions apply to that step. Only include a skill when it is directly relevant — unrelated skills add noise. Omit `profile.skills` (or leave it empty) to reuse the full task-scope pool for that step. Never invent skills that are not listed under AVAILABLE-SKILLS; unknown names are dropped.
