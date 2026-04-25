# Prompt Review Report

**Date:** 2026-04-25
**Scope:** Comprehensive review of all LLM prompts used in the c0wrk codebase
**Repository:** `/Users/vkochetkov/Repositories/c0wrk`

---

## Executive Summary

The c0wrk codebase uses **41 distinct prompt artifacts** across three categories:
- **29 standalone `.md` files** under `core/prompts/` and `core/tools/prompts/`, embedded via `//go:embed`
- **11 hardcoded Go string constants** in `core/planner.go`
- **1 hardcoded inline string** in `core/systemprompt.go`

The prompt architecture is well-organized with a strong layering pattern: base prompts + provider-specific overlays + a universal verification mandate. However, several prompts exhibit structural inconsistencies, missing context instructions, and opportunities for improved clarity.

---

## Prompt Inventory & Analysis

### Category 1: Standalone `.md` Prompts (29 files)

---

#### 1. `core/prompts/compaction_summarize.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/compaction_summarize.md`
**Embedded via:** `core/prompts/prompts.go` line 34 → `CompactionSummarize`

**Summary:** Instructs the LLM to produce a dense structured summary of a conversation for context window management. The prompt defines required sections (Overarching Goal, Progress, Files Touched, Key Decisions, etc.) and mandates JSON output.

**Strengths:**
- Very specific output format with discrete required sections
- Explicit directives: "Do NOT prefix with markdown fences" and "Output ONLY the JSON object"
- Clear structure with horizontal rule separators
- Edge case handling: explicit handling of empty sections

**Weaknesses:**
- No guidance on summary length or token budget
- No instructions for handling very long conversations where even the summary might exceed limits
- Missing "what NOT to include" guidance (e.g., should tool call results be summarized or omitted?)
- No priority ordering for sections — key decisions should arguably appear before file lists

**Recommendations:**
- Add a token budget target (e.g., "Aim for under 1500 tokens")
- Add guidance on truncation strategy for extremely long conversations
- Prioritize sections: Goal → Progress → Decisions → Files → Findings → Errors
- Add a "DO NOT include" section: tool call arguments, full file contents, verbose error stacks beyond 1 line

---

#### 2. `core/prompts/orchestrator_system.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/orchestrator_system.md`
**Embedded via:** `core/prompts/prompts.go` line 21 → `OrchestratorSystem`

**Summary:** The core system prompt for the orchestrator agent. Covers the ReAct loop, tool priority tiers, search efficiency, output strategy, bash_exec output management, safety rules, and environment context. This is the most complex prompt in the system (~230 lines).

**Strengths:**
- Excellent tool priority tier system (Tier 1: semantic search, Tier 2: ripgrep/glob, Tier 3: file ops, Tier 4: bash)
- Clear "Search Efficiency" section with a mental budget (5 searches max before strategy switch)
- Strong epistemic discipline section mandating verification through tools
- Concrete safety rules for file operations
- Well-structured with markdown headings

**Weaknesses:**
- Very long — may consume significant context window budget before user context is even added
- The "Plan Context" section is a massive block of instructions that appears to be largely duplicated from the workspace/system prompt sections
- "Output Strategy" and "Tool Call Communication" overlap in content (both discuss finish tool)
- No explicit guidance on when to use `ask_user` vs proceed autonomously
- Environment section at the bottom is disconnected from the rest
- The "Relevant Project Files" auto-append section appears after the prompt proper — its role as the last thing the LLM sees is good but it's not in the .md file itself

**Recommendations:**
- Consider splitting into a "core rules" section that always applies and a "context-dependent" section that can be trimmed
- Consolidate duplicate finish tool instructions into one clear location
- Add a "When to ask the user" decision tree
- Move the language instruction ("Reason in English. Your final answer MUST match the user's language") to a more prominent location — it's currently buried mid-prompt
- Add a "MUST NOT" summary checklist at the top or bottom

---

#### 3. `core/prompts/orchestrator_plan_context.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/orchestrator_plan_context.md`
**Embedded via:** `core/prompts/prompts.go` line 22 → `OrchestratorPlanContext`

**Summary:** Appended to the system prompt when the agent is operating in Plan Mode. Instructs the agent that it is executing one step of a larger plan, not the whole task.

**Strengths:**
- Clear scoping: "Complete ONLY this step's objective"
- Good integration points: references `read_step_output` and `list_step_outputs` tools
- Reminds about the `finish` tool for delivering step results
- References "Original User Request" for context

**Weaknesses:**
- Very brief — doesn't explain how to interpret dependency steps or handle step failure
- No guidance on partial completion or error reporting when a step can't be fully completed
- Doesn't mention how to report step status (success/failure/partial) in the finish output
- The phrase "Do NOT produce final deliverables or perform work belonging to other steps" could conflict with tasks where producing a deliverable IS the step

**Recommendations:**
- Add explicit guidance on error handling: "If your step cannot be completed, report the specific blocker and what was achieved"
- Add a status reporting convention for the `finish` call
- Clarify the relationship between step output and the "Original User Request" context
- Add guidance on when a step should call `ask_user` vs `finish` with partial results

---

#### 4. `core/prompts/reflector_system.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/reflector_system.md`
**Embedded via:** `core/prompts/prompts.go` line 23 → `ReflectorSystem`

**Summary:** System prompt for the reflector agent that analyzes completed plan steps and provides guidance for subsequent steps.

**Strengths:**
- Clear output format: "MUST respond with JSON: `{"reflection": "…", "suggestions": ["…"]}`
- Good inventory of what the reflector should NOT do (no new steps, no re-execution)
- Explicit context about what information is available (step_plan, step ID, the output)

**Weaknesses:**
- Very terse — only 2 sentences of instruction before the JSON format spec
- No guidance on what makes a good reflection vs a poor one
- No examples of good reflections in different scenarios (success, partial, failure)
- Missing guidance on suggestion specificity: should suggestions reference specific files, tools, or patterns?
- No instructions on how to use the available context (e.g., should it reference the original user request?)

**Recommendations:**
- Add 2-3 examples of good reflections for different outcome scenarios
- Add a "Reflection Quality Checklist" (Is it specific? Does it reference concrete failures? Does it suggest next actions?)
- Instruct the reflector to reference specific files, tool names, and error messages from the step output
- Add guidance on maintaining reflection brevity

---

#### 5. `core/prompts/router_system.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/router_system.md`
**Embedded via:** `core/prompts/prompts.go` line 24 → `RouterSystem`

**Summary:** System prompt for the router/intent classifier that categorizes user messages into semantic modes (chat, architect, plan, code, debug, etc.).

**Strengths:**
- Clear enumerated modes with distinct definitions
- Emphasis on classification accuracy: "It is absolutely critical that ambiguous requests are categorized conservatively"
- Good default behavior: chat mode for ambiguous inputs
- Clean JSON output format

**Weaknesses:**
- Mode definitions are somewhat overlapping (chat vs architect vs plan — all can involve discussion)
- No guidance on confidence levels or multi-mode classification
- Missing edge case: what about multi-intent messages (e.g., "find the bug and explain it")?
- No examples of borderline cases to guide classification
- The mode list is hardcoded — changes require updating both the .md and the Go enum

**Recommendations:**
- Add 3-5 borderline classification examples with reasoning
- Define a priority order for multi-intent messages (e.g., code+chat → code)
- Add a "confidence" field to the output format (high/medium/low)
- Consider consolidating overlapping modes (chat vs architect) with clearer differentiators

---

#### 6. `core/prompts/verification_mandate.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/verification_mandate.md`
**Embedded via:** `core/prompts/prompts.go` line 25 → `VerificationMandate`

**Summary:** Universal ruleset appended to all system prompts. Covers file operation safety, scope discipline, bash_exec discipline, and content creation rules.

**Strengths:**
- Very clear "MUST" vs "MUST NOT" language
- Concrete rules: specify line range in read_file, verify path before deletion, check file existence before git add
- Good safety focus on destructive operations
- Readable structure with code-block examples

**Weaknesses:**
- The content creation section is overly restrictive for plan-mode steps (where creating files IS the deliverable)
- "Never create new files unless explicitly required" could confuse agents in steps that ARE about creating files
- The grep rule "never grep for giant regex catch-all patterns" is too vague — what qualifies as "giant"?
- Missing guidance on temporary/intermediate file cleanup

**Recommendations:**
- Add a carve-out for plan-mode steps: "When operating in plan-mode, follow the step's specific instructions regarding file creation"
- Clarify the grep rule with a concrete example of what's too broad
- Add a section on temporary file cleanup: "Delete intermediate files before calling finish unless the step specifies otherwise"
- Consider splitting into "always apply" safety rules and "context-dependent" discipline rules

---

#### 7-22. Planner Provider Variants (16 files)

**Locations (all under `/Users/vkochetkov/Repositories/c0wrk/core/prompts/`):**

| File | Variable in `prompts.go` |
|------|--------------------------|
| `planner_base.md` | `PlannerBase` (line 15) |
| `planner_replan.md` | `PlannerReplan` (line 16) |
| `planner_informed.md` | `PlannerInformed` (line 17) |
| `planner_default.md` | Embedded via `FamilyPrompt("planner", family)` |
| `planner_anthropic.md` | `PlannerAnthropic` (line 27) → `FamilyPrompt("planner", "anthropic")` |
| `planner_openai_flagship.md` | `PlannerOpenAIFlagship` → `FamilyPrompt("planner", "openai_flagship")` |
| `planner_openai_standard.md` | `PlannerOpenAIStandard` |
| `planner_gemini.md` | `PlannerGemini` |
| `planner_deepseek.md` | `PlannerDeepSeek` |
| `planner_mistral.md` | `PlannerMistral` |
| `planner_kimi.md` | `PlannerKimi` |
| `planner_openai_codex.md` | `PlannerOpenAICodex` |
| `orchestrator_default.md` | `OrchestratorDefault` (line 28) |
| `orchestrator_anthropic.md` | `OrchestratorAnthropic` |
| `orchestrator_openai_flagship.md` | `OrchestratorOpenAIFlagship` |
| `orchestrator_gemini.md` | `OrchestratorGemini` |

**Summary:** These are provider-specific overlays that customize the base system prompt for different LLM families (Anthropic Claude, OpenAI GPT-4/GPT-3.5, Google Gemini, DeepSeek, Mistral, Kimi, OpenAI Codex). The planner variants adjust thinking/reasoning instructions; the orchestrator variants adjust tool usage patterns.

**Common Strengths Across All Provider Variants:**
- Clean separation from base prompt (layered architecture)
- Model-specific behavior tuning (e.g., thinking blocks for Claude, structured output for GPT-4)
- Consistent naming convention

**Common Weaknesses Across All Provider Variants:**
- **Minimal differentiation:** Many variants are nearly identical. For instance, `orchestrator_default.md`, `orchestrator_openai_standard.md`, `orchestrator_deepseek.md`, `orchestrator_mistral.md`, and `orchestrator_kimi.md` are all just the single line: "Always use structured JSON output when calling tools." The only truly distinct variants are Anthropic (thinking blocks), OpenAI Flagship (structured output + parallel tool calls), Gemini (thinking + function calling note), and OpenAI Codex (thinking + code-specific).
- **Planner variants are empty or nearly empty:** All 8 provider-specific planner variants (`planner_anthropic.md` through `planner_openai_codex.md`) are **empty strings** — they contain no content at all, meaning the `FamilyPrompt("planner", family)` call returns nothing. This is likely a placeholder pattern, but it means the planner has NO model-specific tuning.
- **Missing documentation:** No comments explain what each variant is supposed to optimize, making it hard to maintain or extend.
- **No differentiation by model capability:** There's no variant for models with very small context windows or limited tool support.

**Recommendations for Provider Variants:**
- **Fill in planner variants:** Add model-specific planner instructions. For example, Anthropic variants could instruct the planner to use Claude's extended thinking; Gemini variants could recommend structured output mode.
- **Consolidate near-identical orchestrator variants:** Create a shared "standard" template and only create separate files for truly different behavior (Anthropic, OpenAI Codex).
- **Add variant documentation:** Each file should have a comment header explaining what it optimizes and why.
- **Consider capability-based variants:** Instead of being purely provider-based, consider variants for "large context window", "good at JSON", "good at reasoning", etc.

**Specific Analysis — Planner Base:**

**Location:** `core/prompts/planner_base.md`

**Summary:** The foundational planner prompt. Instructs the LLM to decompose user tasks into DAG execution plans with steps, dependencies, and tool lists.

**Strengths:**
- Clear DAG planning approach (steps with depends_on, parallelizable flag)
- Good step structure: What, How, Where, Acceptance Criteria
- Profile-based step assignment (role + allowed_tools + domain)
- Concrete tool lists by profile
- Good "Thinking Requirements" section enforcing reasoning before output

**Weaknesses:**
- Extremely long — this prompt alone is substantial and must be combined with Go-injected sections
- The domain assignment and agent profiles sections could conflict with the hardcoded Go constants (`planModeDomainAssignment`, `planModeAgentProfiles`) that are also injected
- No guidance on step granularity: when should a task be one step vs five?
- The parallelization guidance is vague: "Consider parallelization opportunities" without concrete criteria
- Profile tool lists may become stale if new tools are added

**Recommendations:**
- Add section on step granularity with examples
- Make parallelization guidance concrete: "Steps can run in parallel if they operate on different files AND have no dependency relationship"
- Consider extracting profile definitions into a separate config file rather than hardcoding in the prompt
- Add guidance on maximum plan size (how many steps are reasonable)

**Specific Analysis — Planner Replan:**

**Location:** `core/prompts/planner_replan.md`

**Summary:** Instructions for replanning when the original plan needs adjustment.

**Strengths:**
- Clear instructions to create entirely new steps with minimal references to old ones
- Explicit handling of the "where to start" problem
- Good constraint against infinite remapping of step IDs

**Weaknesses:**
- No explicit criteria for when replanning should be triggered
- No guidance on preserving completed work
- The instruction "Old Step IDs MUST NOT appear in the new plan" is too absolute — sometimes a rebuilt step reuses the same tool chain

**Recommendations:**
- Add a "When to Replan" section with concrete triggers (unrecoverable tool failure, structural change in requirements, etc.)
- Add guidance on preserving completed outputs
- Soften the "MUST NOT" to "SHOULD avoid reusing old step IDs unless the step is identical in purpose"

**Specific Analysis — Planner Informed:**

**Location:** `core/prompts/planner_informed.md`

**Summary:** Planner guidance when the planner has access to exploration context (codebase knowledge from search_graph, semantic_search, etc.).

**Strengths:**
- Good integration with codebase exploration tools
- Explicit instruction to reference file paths found during exploration
- Links the exploration phase to concrete step content

**Weaknesses:**
- Very brief — assumes the LLM knows how to extract relevant information from exploration output
- No guidance on how to handle conflicting information from multiple exploration sources
- Missing instruction on when to re-explore vs trust existing findings

**Recommendations:**
- Add a "How to use exploration output" section with a structured checklist
- Add guidance on confidence levels for exploration findings
- Clarify the relationship between exploration and final plan steps

---

#### 23. `core/prompts/prompt_optimize_extract.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/prompt_optimize_extract.md`
**Embedded via:** `core/prompts/prompts.go` line 36 → `PromptOptimizeExtract`

**Summary:** First pass of the two-pass prompt optimization flow. Translates a user prompt into English (if needed), extracts multilingual keywords, decomposes into subtasks, identifies missing context, and suggests context tool usage.

**Strengths:**
- Well-defined JSON output format
- Good keyword extraction strategy (original language + English)
- Task decomposition into actionable subtasks
- Context gap analysis with tool suggestions
- Exhaustive scenario table for tool selection

**Weaknesses:**
- The keyword extraction instruction is confusing: "keywords for semantic search (in the original language)" contradicts the later requirement for "English keywords for codebase investigation"
- The scenarios table is very long and may distract from the core task
- No guidance on how many keywords to extract (risk of too many or too few)
- Missing instructions on handling ambiguous prompts where the intent is genuinely unclear

**Recommendations:**
- Simplify the keyword section: clearly separate "original language keywords for translation" from "English keywords for codebase search"
- Move the scenarios table to a reference appendix or inline it more compactly
- Set keyword count bounds: "Extract 5-15 keywords total, prioritize specificity over quantity"
- Add fallback behavior: "If intent is genuinely ambiguous, flag it in missing_context rather than guessing"

---

#### 24. `core/prompts/prompt_optimize_rewrite.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/prompts/prompt_optimize_rewrite.md`
**Embedded via:** `core/prompts/prompts.go` line 37 → `PromptOptimizeRewrite`

**Summary:** Second pass of prompt optimization. Takes the extraction output and produces the final optimized prompt with structure guidance.

**Strengths:**
- Clear output: "Output ONLY the final optimized prompt"
- Good structural checklist (markdown, bullet points, numbered steps, YAML)
- Context injection examples
- Anti-pattern list is valuable

**Weaknesses:**
- The anti-patterns section is good but could be expanded with more examples
- No guidance on length constraints — optimized prompts could become too long
- Missing instruction on preserving the user's original intent while reorganizing
- The summary says it "guides the agent to use available codebase context" but the structural recommendations are generic

**Recommendations:**
- Add token budget guidance: "Target under 2000 tokens for the optimized prompt"
- Add a "Preserve Intent" rule: "Do not change what the user asked for — only how it's expressed"
- Add more concrete anti-pattern examples (e.g., "Bad: 'fix it' → Good: 'fix the NPE in UserService.createUser on line 47'")
- Include guidance on when NOT to optimize (already well-structured prompts)

---

#### 25. `core/tools/prompts/judge_system.md`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/tools/prompts/judge_system.md`
**Embedded via:** `core/tools/prompts/prompts.go` line 5 → `JudgeSystem`

**Summary:** System prompt for the judge/evaluator agent that assesses code quality, correctness, and completeness.

**Strengths:**
- Clear evaluation dimensions
- Structured scoring format

**Weaknesses:**
- Very brief — only a few sentences of instruction
- Scoring criteria are vague: what constitutes a "good" score vs "poor"?
- No examples of evaluation outputs
- Missing guidance on how to handle ambiguous or incomplete code submissions

**Recommendations:**
- Add a scoring rubric with concrete criteria for each dimension
- Add 2-3 example evaluations at different quality levels
- Add guidance on constructive feedback format
- Define what "completeness" means in the context of code review

---

### Category 2: Hardcoded Prompt Strings in Go Source Files

---

#### 26-36. Planner Constants in `core/planner.go`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/planner.go`, lines 33-151

These are 11 Go string constants that form the planner's system prompt:

| Constant | Lines | Purpose |
|----------|-------|---------|
| `planModePreamble` | 33-45 | Task planner role and DAG decomposition instructions |
| `planModeDomainAssignment` | 46-66 | Domain assignment rules for steps |
| `planModeAgentProfiles` | 68-76 | Agent profile definitions (architect, coder, explorer) |
| `planModeExtraSections` | 78-117 | Step format specification, dependency rules, pre-execution validation |
| `planModeTail` | 120-121 | "REFLECTIONS" section header |
| `planModeJSONExample` | 122-123 | JSON example for step output |
| `continuationModePreamble` | 130-145 | Continuation planner preamble |
| `continuationModeExtraSections` | 147 | Empty string (placeholder) |
| `continuationModeTail` | 148 | Empty string (placeholder) |
| `continuationModeJSONExample` | 149-151 | JSON example for continuation step output |

**Strengths:**
- Well-organized into discrete sections that are assembled programmatically
- Placeholders (`TASK-DESCRIPTION`, `TERMINAL-STEPS`, `REFLECTIONS`) enable template-based assembly
- JSON examples provide clear format expectations
- Good separation between plan mode and continuation mode

**Weaknesses:**
- **Duplication with .md files:** `planModePreamble` and `planModeDomainAssignment` overlap with `planner_base.md` content. The planner_base.md already covers task decomposition, domain assignment, and agent profiles. Having these in both locations creates a maintenance burden.
- **Missing context usage instructions:** None of the planner constants instruct the LLM on how to use available tools (search_graph, semantic_search, read_file) for codebase exploration during planning.
- **Static tool lists:** The agent profile tool lists are hardcoded. Adding a new tool requires updating these constants AND the registry.
- **Empty placeholders:** `continuationModeExtraSections` and `continuationModeTail` are empty strings — they should either be populated or removed to avoid confusion.
- **JSON example is monolithic:** The JSON example is a single-line string, making it hard to read and maintain.

**Recommendations:**
- **Deduplicate:** Remove overlapping content between planner_base.md and the Go constants. Choose one canonical location — preferably the .md files for long-form content and Go constants for short, dynamic sections.
- **Dynamic profiles:** Generate the agent profile section from the tool registry at runtime rather than hardcoding.
- **Add exploration guidance:** Include instructions like: "Before finalizing a plan, use search_graph and semantic_search to verify file paths and code structure."
- **Fill or remove empty constants:** Either provide content for `continuationModeExtraSections` and `continuationModeTail` or remove them.
- **Break JSON example into readable multi-line format** for better maintainability.

---

#### 37. ReAct Mode Completion Instruction in `core/systemprompt.go`

**Location:** `/Users/vkochetkov/Repositories/c0wrk/core/systemprompt.go`, lines 140-143

```go
result += "\n\n## Completion\nYou are operating in single-step mode. When you have completed your work, you MUST call the `finish` tool with your final answer. Do not simply respond with text — the system only recognizes task completion through an explicit `finish` tool call."
```

**Summary:** Appended to the system prompt when operating in ReAct mode (non-plan). Reinforces that the `finish` tool must be called.

**Strengths:**
- Very direct and unambiguous
- Explains WHY the finish tool is needed (system only recognizes explicit calls)
- Uses the `## Completion` heading for visibility

**Weaknesses:**
- **Inconsistent location:** This is the only hardcoded prompt string in `systemprompt.go`. All other non-planner prompts are in .md files. This breaks the pattern.
- **Duplicates orchestrator_system.md:** The orchestrator system prompt already has a "## Completion" section in its "Output Strategy" block that covers the finish tool. This adds a SECOND completion section that could confuse the LLM.
- **Missing edge case:** What if the agent encounters an unrecoverable error? Should it still call `finish` with an error report, or are there alternatives?
- **The term "single-step mode"** could be misleading — ReAct mode still involves multiple internal steps (Thought → Action → Observation cycles).

**Recommendations:**
- **Move to .md file:** Extract this into a `orchestrator_react_completion.md` file for consistency with the rest of the prompt architecture.
- **Remove duplication:** The orchestrator_system.md already covers the finish tool requirement. Either remove this injected string or consolidate into one location.
- **Clarify "single-step":** Change to "You are in single-task mode (no multi-step plan)" to avoid confusion with the Thought→Action→Observation loop.
- **Add error handling:** "If you encounter an unrecoverable error, call `finish` with a clear description of the failure and what was attempted."

---

### Category 3: Non-Prompt Markdown Files (for reference)

These files exist in the repository but are documentation, not LLM prompts:

| File | Purpose |
|------|---------|
| `AGENTS.md` | Developer guidance for coding agents working on c0wrk |
| `README.md` | Project README |
| `TODO.md` | Project task list |
| `specs/prompt-optimization-spec.md` | Design specification for the prompt optimization system |
| `specs/prompt-optimization-roadmap.md` | Roadmap for prompt optimization features |

---

## Overall Patterns and Priority Improvements

### Pattern 1: **Duplication Between .md Files and Go Constants**

The planner prompt content exists in two places: `planner_base.md` (embedded) and the Go constants in `planner.go`. The `planModePreamble`, `planModeDomainAssignment`, and `planModeAgentProfiles` constants overlap with the .md file content. This creates a maintenance burden — any change to the planner's instructions requires updating both locations, and it's easy for them to drift apart with contradictory instructions.

**Priority Improvement:** Consolidate all planner instructions into the .md files, keeping only truly dynamic content (like `TASK-DESCRIPTION` placeholders) in Go. The .md files should be the single source of truth for all prompt text.

---

### Pattern 2: **Empty/Placeholder Variants Provide No Value**

All 8 planner provider-specific variants (`planner_anthropic.md` through `planner_openai_codex.md`) are empty. The provider-specific variant system calls `FamilyPrompt("planner", family)` which returns an empty string for all planner families. This means planner behavior is identical regardless of the LLM provider. Additionally, 5 orchestrator variants are effectively identical (single-line JSON output instruction).

**Priority Improvement:** Either (a) populate all planner variants with model-specific instruction tuning (e.g., Claude gets thinking guidance, Gemini gets structured output instructions), or (b) remove the empty variant files and simplify the `FamilyPrompt` function to return empty for planner families until variants are actually needed. For orchestrator, consolidate the near-identical variants into a shared template.

---

### Pattern 3: **Missing Context Usage Instructions in Optimization Prompts**

The `prompt_optimize_extract.md` and `prompt_optimize_rewrite.md` prompts are designed for a prompt optimization flow, yet neither explicitly instructs the LLM to use codebase exploration tools (search_graph, semantic_search, glob, ripgrep) to discover relevant files, symbols, or patterns. The extract prompt mentions context tools abstractly in its scenario table but doesn't give the agent actionable instructions for using them.

**Priority Improvement:** Add explicit tool usage instructions to both optimization prompts:
- In `prompt_optimize_extract.md`: "Before finalizing the extraction, use `search_graph` with relevant keywords to discover existing code patterns, and use `semantic_search` to find related code. Incorporate found file paths and function names into the suggested_context."
- In `prompt_optimize_rewrite.md`: "When injecting codebase context, reference specific files and symbols found via exploration tools. Use `search_graph` to verify symbol existence before including them."

---

### Pattern 4: **Inconsistent Completion/Error Handling Guidance**

The `finish` tool requirement is mentioned in at least 5 different locations (orchestrator_system.md, orchestrator_plan_context.md, systemprompt.go hardcoded string, planner_base.md, verification_mandate.md). Each location says it slightly differently, and none comprehensively covers error scenarios. An agent encountering a failure may not know whether to call `finish` with an error, retry, or escalate.

**Priority Improvement:** Create a single "Completion and Error Handling" section (or separate .md file) that covers:
1. Successful completion → `finish` with results
2. Partial completion → `finish` with what was achieved + what remains
3. Unrecoverable error → `finish` with error description and attempted steps
4. Need clarification → `ask_user` instead of guessing
Include this section in the orchestrator system prompt and reference it from all other prompts.

---

### Pattern 5: **Prompt Length Management**

The orchestrator system prompt is approximately 230 lines. Combined with the verification mandate (~80 lines), plan context overlay (~25 lines), provider-specific overlay, workspace context, environment block, and auto-RAG hints, the full system prompt can exceed 400 lines before any user content is added. For models with small context windows, this leaves little room for the actual task.

**Priority Improvement:** Implement a tiered prompt loading strategy:
- **Tier 1 (Always):** Core safety rules, tool usage basics, finish requirement (~50 lines)
- **Tier 2 (Context-dependent):** Tool priority tiers, search efficiency, output strategy (~100 lines)
- **Tier 3 (Opt-in):** Plan context, provider overlays, active skills
This would allow dynamic trimming of the system prompt based on available context window budget.

---

### Pattern 6: **Lack of Examples in Non-Orchestrator Prompts**

The orchestrator system prompt is rich with concrete examples (DO/DON'T rules, tool call formats, etc.). However, the reflector, router, judge, and optimizer prompts have minimal or no examples. This is particularly problematic for the reflector and judge, where output quality heavily depends on understanding what "good" looks like.

**Priority Improvement:** Add 2-3 annotated examples to each of: reflector_system.md, router_system.md, judge_system.md, prompt_optimize_extract.md, and prompt_optimize_rewrite.md. Examples should cover both ideal outputs and common mistakes.

---

## Summary Table

| # | Prompt | Location | Type | Status | Key Issue |
|---|--------|----------|------|--------|-----------|
| 1 | Compaction Summarize | `core/prompts/compaction_summarize.md` | .md | ✅ Good | Missing token budget |
| 2 | Orchestrator System | `core/prompts/orchestrator_system.md` | .md | ✅ Good | Too long, some duplication |
| 3 | Orchestrator Plan Context | `core/prompts/orchestrator_plan_context.md` | .md | ⚠️ Fair | Missing error handling |
| 4 | Reflector System | `core/prompts/reflector_system.md` | .md | ⚠️ Fair | Needs examples |
| 5 | Router System | `core/prompts/router_system.md` | .md | ⚠️ Fair | Overlapping modes, no examples |
| 6 | Verification Mandate | `core/prompts/verification_mandate.md` | .md | ✅ Good | Plan-mode carve-out needed |
| 7 | Planner Base | `core/prompts/planner_base.md` | .md | ✅ Good | Overlaps with Go constants |
| 8 | Planner Replan | `core/prompts/planner_replan.md` | .md | ⚠️ Fair | Missing replan triggers |
| 9 | Planner Informed | `core/prompts/planner_informed.md` | .md | ⚠️ Fair | Too brief |
| 10-17 | Planner Provider Variants (8) | `core/prompts/planner_*.md` | .md (empty) | ❌ Poor | All empty — no value |
| 18 | Orchestrator Default | `core/prompts/orchestrator_default.md` | .md | ✅ Good | Used as fallback |
| 19-26 | Orchestrator Provider Variants (8) | `core/prompts/orchestrator_*.md` | .md | ⚠️ Fair | 5 are near-identical |
| 27 | Prompt Optimize Extract | `core/prompts/prompt_optimize_extract.md` | .md | ✅ Good | Missing tool usage instructions |
| 28 | Prompt Optimize Rewrite | `core/prompts/prompt_optimize_rewrite.md` | .md | ✅ Good | Missing context usage instructions |
| 29 | Judge System | `core/tools/prompts/judge_system.md` | .md | ⚠️ Fair | Needs rubric and examples |
| 30-40 | Planner Go Constants (11) | `core/planner.go:33-151` | Go string | ⚠️ Fair | Duplicates .md content |
| 41 | ReAct Completion | `core/systemprompt.go:140-143` | Go string | ⚠️ Fair | Breaks .md pattern, duplicates orchestrator |

---

## Conclusion

The c0wrk prompt architecture has strong foundations: a layered design with base prompts and provider overlays, good use of `//go:embed` for separation of concerns, and a universal verification mandate. The orchestrator system prompt is exceptionally well-crafted with clear tool priority tiers and epistemic discipline rules.

The primary areas for improvement are: (1) eliminating duplication between .md files and Go constants, (2) populating or removing the 8 empty planner provider variants, (3) adding context exploration tool instructions to the optimization prompts, (4) creating unified completion/error handling guidance, and (5) adding concrete examples to under-documented prompts (reflector, router, judge). Addressing these would significantly improve prompt maintainability and agent reliability.
