# Prompt Review Report

**Generated:** 2026-04-26

---

## 1. Inventory Table

### 1.1 Standalone `.md` Prompt Files

| # | File Path | Lines | Type | Category |
|---|-----------|-------|------|----------|
| 1 | `core/prompts/orchestrator_system.md` | 1–125 | System Prompt | Orchestrator — Core |
| 2 | `core/prompts/orchestrator_plan_context.md` | 1–9 | Injected Section | Orchestrator — Plan Mode |
| 3 | `core/prompts/orchestrator_default.md` | 1–18 | Family Overlay | Orchestrator — Default |
| 4 | `core/prompts/orchestrator_anthropic.md` | 1–14 | Family Overlay | Orchestrator — Anthropic |
| 5 | `core/prompts/orchestrator_openai_flagship.md` | 1–17 | Family Overlay | Orchestrator — OpenAI Flagship |
| 6 | `core/prompts/orchestrator_openai_standard.md` | 1–14 | Family Overlay | Orchestrator — OpenAI Standard |
| 7 | `core/prompts/orchestrator_gemini.md` | 1–16 | Family Overlay | Orchestrator — Gemini |
| 8 | `core/prompts/orchestrator_deepseek.md` | 1–12 | Family Overlay | Orchestrator — DeepSeek |
| 9 | `core/prompts/orchestrator_mistral.md` | 1–14 | Family Overlay | Orchestrator — Mistral |
| 10 | `core/prompts/orchestrator_kimi.md` | 1–13 | Family Overlay | Orchestrator — Kimi |
| 11 | `core/prompts/orchestrator_openai_codex.md` | 1–21 | Family Overlay | Orchestrator — OpenAI Codex |
| 12 | `core/prompts/planner_base.md` | 1–25 | Template | Planner — Base |
| 13 | `core/prompts/planner_replan.md` | 1–28 | Template | Planner — Replan |
| 14 | `core/prompts/planner_informed.md` | 1–54 | Template | Planner — Informed |
| 15 | `core/prompts/planner_default.md` | 1–12 | Family Overlay | Planner — Default |
| 16 | `core/prompts/planner_anthropic.md` | 1–6 | Family Overlay | Planner — Anthropic |
| 17 | `core/prompts/planner_openai_flagship.md` | 1–12 | Family Overlay | Planner — OpenAI Flagship |
| 18 | `core/prompts/planner_openai_standard.md` | 1–10 | Family Overlay | Planner — OpenAI Standard |
| 19 | `core/prompts/planner_gemini.md` | 1–14 | Family Overlay | Planner — Gemini |
| 20 | `core/prompts/planner_deepseek.md` | 1–6 | Family Overlay | Planner — DeepSeek |
| 21 | `core/prompts/planner_mistral.md` | 1–6 | Family Overlay | Planner — Mistral |
| 22 | `core/prompts/planner_kimi.md` | 1–13 | Family Overlay | Planner — Kimi |
| 23 | `core/prompts/planner_openai_codex.md` | 1–19 | Family Overlay | Planner — OpenAI Codex |
| 24 | `core/prompts/prompt_optimize_extract.md` | 1–12 | One-shot | Prompt Optimizer — Extract |
| 25 | `core/prompts/prompt_optimize_rewrite.md` | 1–19 | One-shot | Prompt Optimizer — Rewrite |
| 26 | `core/prompts/reflector_system.md` | 1–73 | System Prompt | Reflector Agent |
| 27 | `core/prompts/router_system.md` | 1–55 | System Prompt | Router Agent |
| 28 | `core/prompts/verification_mandate.md` | 1–20 | Injected Section | Epistemic Discipline |
| 29 | `core/prompts/compaction_summarize.md` | 1–27 | One-shot | Compaction Summarizer |
| 30 | `core/tools/prompts/judge_system.md` | 1–32 | System Prompt | Tool Safety Judge |

### 1.2 Hardcoded Prompts in Go Source

| # | Location | Lines | Type |
|---|----------|-------|------|
| 31 | `core/systemprompt.go` | 97–103 | Workspace Context Template |
| 32 | `core/systemprompt.go` | 126 | ReAct Single-Step Completion Prompt |
| 33 | `core/systemprompt.go` | 134–146 | Vector Search Hints Section Header |
| 34 | `core/systemprompt.go` | 149–165 | Active Skills Section Header |
| 35 | `core/stepconfig.go` | 16–18 | Role-Specific System Prompt Suffixes (researcher, coder, tester) |
| 36 | `core/planner.go` | 33–48 | `planModePreamble` — Planners: MODE-PREAMBLE injection |
| 37 | `core/planner.go` | 52–70 | `planModeDomainAssignment` — Planners: DOMAIN-ASSIGNMENT injection |
| 38 | `core/planner.go` | 73–80 | `planModeAgentProfiles` — Planners: AGENT-PROFILES injection |
| 39 | `core/planner.go` | 82–119 | `planModeExtraSections` — Planners: step structure + output expectations + research decomposition |
| 40 | `core/planner.go` | 125 | `planModeJSONExample` — Planners: JSON output example |
| 41 | `core/planner.go` | 130–153 | `continuationModePreamble` — Continuation planner preamble |
| 42 | `core/planner.go` | 159 | `continuationModeJSONExample` — Continuation JSON example |
| 43 | `core/builder.go` | 478–490 | Prompt Optimization User Message Template (Step C) |
| 44 | `core/tools/judge.go` | ~130 | Judge User Prompt Template (`"Task: …\n\nTool: …\n\nInput: …"`) |
| 45 | `sdk/tools/envinfo.go` | `FormatFullEnvBlock()` | Full Environment Block (executor/planner) |
| 46 | `sdk/tools/envinfo.go` | `FormatCompactEnvBlock()` | Compact Environment Block (evaluator/judge/reflector) |

**Total: 46 distinct prompts.**

---

## 2. Individual Prompt Analysis

---

### 2.1 `orchestrator_system.md` — Core Orchestrator System Prompt

**File:** `core/prompts/orchestrator_system.md` (lines 1–125, 6089 bytes)

**Summary:** The foundational system prompt for all executor agents. Establishes the ReAct loop, a four-tier tool priority system (semantic_search first, then text search, then file ops, then bash), fact memory usage, output strategy (finish tool mandate), safety rules, language policy, and user interaction protocol.

**Strengths:**
- Exceptionally well-structured with clear section headings and concrete examples (bash command flags, git incantations).
- The tool priority tier system is explicit, actionable, and directly reduces wasted tool calls.
- The fact memory guidance is practical and battle-tested: "store early and often," "store before context grows large," "search before each new subtask."
- Search efficiency budget concept (5 searches then switch strategy) prevents fruitless loops.
- Output strategy is crystal clear about when to write files vs. pass through `finish`.

**Weaknesses:**
- Very long (125 lines, ~6KB). When combined with family overlays, verification mandate, workspace context, environment block, vector hints, and active skills, the resulting prompt can exceed 9KB. Some models (especially smaller ones) may struggle with attention distribution across this volume.
- The "Tool Call Communication" section mandates visible text before every tool call but doesn't address the scenario where an agent chains multiple independent calls that would be better batched.
- The safety section is minimal ("verify targeting the correct path"). No guidance on destructive operations beyond file ops.
- No explicit instruction on what to do when the user's language cannot be determined from the input.

**Recommendations:**
1. **Add a "Minimum Viable Prompt" variant** (~40 lines) for contexts where token budget is tight, keeping only ReAct loop, finish mandate, tool priority, and search efficiency.
2. **Expand safety** to cover `bash_exec` risks (e.g., "never run `rm -rf`, `sudo`, `chmod 777`; for destructive bash commands, explain what each part does").
3. **Clarify batching**: "When you have multiple independent tool calls, you may batch them in a single `<tool_calls>` block to reduce round trips."
4. **Language fallback**: "If you cannot determine the user's language, default to English and note this in your finish output."

---

### 2.2 `orchestrator_plan_context.md` — Plan Mode Context

**File:** `core/prompts/orchestrator_plan_context.md` (lines 1–9, 719 bytes)

**Summary:** Injected when an executor is running within a plan. Reminds the agent it's executing one step of a larger plan, instructs it to verify acceptance criteria before calling `finish`, and documents `read_step_output`/`list_step_outputs`.

**Strengths:**
- Concise and focused. Exactly what a step executor needs.
- The verification rule ("use tool calls to confirm each criterion — do not rely on assumptions") is directly actionable.
- Cross-step data access instructions are clear and minimal.

**Weaknesses:**
- Does not address what happens when a dependency step's output is insufficient — only says "access full outputs," not what to do if the output is genuinely incomplete.
- No guidance on partial success: what if 3 of 5 acceptance criteria pass?

**Recommendations:**
1. **Add partial success handling**: "If some criteria pass but others fail, call `finish` with both accomplishments and remaining gaps clearly labeled — the planner will decide on replan/retry."
2. **Add insufficiency guidance**: "If a dependency step's output is missing critical information, state what is missing in your finish output rather than attempting to guess or re-derive it."

---

### 2.3 `orchestrator_default.md` — Default Family Overlay

**File:** `core/prompts/orchestrator_default.md` (lines 1–18, 883 bytes)

**Summary:** A general-purpose overlay for models not matching a specific family. Emphasizes holistic reasoning, uncertainty handling, and checking for project instruction files.

**Strengths:**
- The uncertainty handling section is thoughtful — it gives three distinct scenarios (reasoned hypothesis, contradictory evidence, insufficient data) with clear actions for each.
- "Check for AGENTS.md, CLAUDE.md, CONTEXT.md" is a practical directive that leverages project-level context.

**Weaknesses:**
- The holistic reasoning advice ("explore multiple angles before committing") may encourage over-exploration in already capable models.
- The project context instruction names specific files but doesn't say what to look for in them — it's too vague.
- Redundant with the base system prompt in places (both discuss forming hypotheses).

**Recommendations:**
1. **Add a budget qualifier**: "Explore multiple angles within a reasonable search budget (see Search Efficiency in the system prompt)."
2. **Specify project file guidance**: "In AGENTS.md/CLAUDE.md, look for: coding conventions, test patterns, build commands, and architectural constraints."
3. **De-duplicate** against the base system prompt's reasoning section — consider whether the base prompt should be trimmed and this overlay should carry the burden.

---

### 2.4 `orchestrator_anthropic.md` — Anthropic Family Overlay

**File:** `core/prompts/orchestrator_anthropic.md` (lines 1–14, 607 bytes)

**Summary:** Structured action-observation-assessment cycles. Emphasizes compact output, direct statements, and code citations in `file_path:line_number` format.

**Strengths:**
- The action-observation-assessment cycle is a strong pattern for Claude models that benefit from structured reflection.
- Explicit citation format is actionable and consistent.
- "No motivational filler or exploratory narration" is well-targeted at Anthropic model tendencies.

**Weaknesses:**
- The three-step cycle (Action → Observation → Assessment) overlaps with the ReAct loop described in the base system prompt. Agents may be confused about which cycle to follow.
- "Provide self-contained context when delegating to sub-tasks" assumes sub-task delegation, which isn't part of the orchestration model.

**Recommendations:**
1. **Harmonize with ReAct**: Rephrase as "Within each ReAct iteration, follow this structure: 1) Action with clear purpose, 2) Observation with cited results, 3) Assessment of what changed."
2. **Remove sub-task delegation reference** if the architecture doesn't support it, or define it clearly if it does.

---

### 2.5 `orchestrator_openai_flagship.md` — OpenAI Flagship Overlay

**File:** `core/prompts/orchestrator_openai_flagship.md` (lines 1–17, 735 bytes)

**Summary:** Mandates exhaustive research before modification. Includes a four-item research checklist. Targets OpenAI flagship models (GPT-4, o1, o3).

**Strengths:**
- The research-first mandate directly addresses a common failure mode of flagship models: jumping to implementation without understanding context.
- The checklist is concrete and covers the key areas: patterns, dependencies, side effects, test coverage.
- "When evidence is contradictory, acknowledge explicitly" is excellent epistemic guidance.

**Weaknesses:**
- "NEVER end your turn without having made concrete progress" may conflict with the research-first mandate — what if the research phase took the entire turn?
- The checklist is a flat list; it doesn't prescribe an order or how to determine when research is "exhaustive" enough.

**Recommendations:**
1. **Define "exhaustive"**: Add a guardrail — "Exhaustive means: you have confirmed all four checklist items AND have enough information to make the change without guessing. If after 8 semantic_search/ripgrep calls you still have gaps, proceed with the best available information and note assumptions."
2. **Resolve the progress contradiction**: "If your turn was spent entirely on research, that IS concrete progress — summarize your findings and continue in the next turn."

---

### 2.6 `orchestrator_openai_standard.md` — OpenAI Standard Overlay

**File:** `core/prompts/orchestrator_openai_standard.md` (lines 1–14, 639 bytes)

**Summary:** Pragmatic step-by-step approach. Imposes constraints: 125-char code citations, flat bullet lists only, shifted tool priority.

**Strengths:**
- The read → plan → act → verify cycle is simple and effective for smaller models.
- Constraints (125-char limit, flat lists) are tailored to the output limitations of standard OpenAI models.
- The tool priority adjustment that deprioritizes semantic_search is well-reasoned for models where semantic_search overhead isn't justified.

**Weaknesses:**
- The 125-character citation limit is arbitrary and potentially harmful — important error messages or file paths may be truncated.
- The "flat bullet lists only" rule may reduce expressiveness for genuinely hierarchical findings.

**Recommendations:**
1. **Raise the citation limit to 150 characters** or make it a soft guideline: "Prefer citations under 125 characters; if truncating would lose critical detail, use up to 200."
2. **Add an exception for error output**: "Error messages from build/test commands may exceed the citation limit — include the full first line and note the file if longer."

---

### 2.7 `orchestrator_gemini.md` — Gemini Family Overlay

**File:** `core/prompts/orchestrator_gemini.md` (lines 1–16, 877 bytes)

**Summary:** Mandates absolute paths, workflow mode selection (existing codebase vs. greenfield), and strict convention matching.

**Strengths:**
- The absolute paths mandate is clear and justified — Gemini models benefit from explicit path handling.
- The existing/greenfield workflow modes are well-defined with distinct guidance for each.
- Excellent uncertainty handling: "state explicitly what is missing and why it matters."

**Weaknesses:**
- "Before running bash commands that modify the system, explain what each critical command does" — "critical" is undefined and subjective.
- The convention-matching guidance is generic; it doesn't give concrete examples of what conventions to look for.

**Recommendations:**
1. **Define "critical"**: "Critical commands are those that: write to disk outside the workspace, modify system configuration, install/uninstall packages, or affect running processes."
2. **Add convention examples**: "Conventions to match: indentation style (tabs vs. spaces), naming (camelCase vs. snake_case), file organization (flat vs. package-nested), error handling patterns."

---

### 2.8 `orchestrator_deepseek.md` — DeepSeek Family Overlay

**File:** `core/prompts/orchestrator_deepseek.md` (lines 1–12, 493 bytes)

**Summary:** Hypothesis-driven reasoning. One thread at a time. Validate results after each tool call.

**Strengths:**
- Hypothesis-driven reasoning is a strong paradigm for DeepSeek models, which benefit from explicit expectation-setting.
- "One thread at a time" prevents context fragmentation.
- The execution style section aligns well with DeepSeek's tendencies.

**Weaknesses:**
- Very similar to `orchestrator_default.md` — almost identical phrasing in Reasoning Approach and Execution Style sections. The differentiation is minimal.
- No model-specific guidance that distinguishes DeepSeek from other models.

**Recommendations:**
1. **Add DeepSeek-specific guidance**: e.g., "DeepSeek models excel at structured reasoning — use numbered steps for multi-part analysis and prefer explicit logical connectors (therefore, because, however)."
2. **Differentiate from default**: If the default overlay already covers hypothesis-driven reasoning, this overlay should add value specific to DeepSeek, such as handling of its tokenization quirks or reasoning mode behavior.

---

### 2.9 `orchestrator_mistral.md` — Mistral Family Overlay

**File:** `core/prompts/orchestrator_mistral.md` (lines 1–14, 466 bytes)

**Summary:** One clear sentence plan per action. One action at a time. Simple, flat instructions. Avoid complex conditional reasoning.

**Strengths:**
- The "one clear sentence" and "one action at a time" rules are perfectly tailored to Mistral's strengths (concise, action-oriented).
- "Avoid complex conditional reasoning" is a smart constraint for models that can get tangled in if-else chains.
- "One tool call per reasoning step when possible" provides clear pacing.

**Weaknesses:**
- "Simple, flat instructions — avoid complex conditional reasoning" may be too restrictive for tasks that genuinely require conditional logic (e.g., "if the file exists, modify it; otherwise, create it").
- No guidance on error recovery, which is critical for a model encouraged to take one action at a time.

**Recommendations:**
1. **Soften the conditional ban**: "Prefer simple, flat instructions. When conditional logic is necessary, use at most one level of branching."
2. **Add error recovery**: "If a tool call fails, state the error, identify the likely cause, and try one alternative approach before finishing with a status report."

---

### 2.10 `orchestrator_kimi.md` — Kimi Family Overlay

**File:** `core/prompts/orchestrator_kimi.md` (lines 1–13, 457 bytes)

**Summary:** Explicit verification points structure. Bullet-point output. Complete one task fully before moving to the next.

**Strengths:**
- The four-step verification structure (investigate → execute → report → conclude) is clean and aligned with Kimi's capabilities.
- "Complete one task fully before moving to the next" prevents task fragmentation.

**Weaknesses:**
- The verification structure is described in prose but not illustrated with an example. A concrete example would make it far more actionable.
- "Use bullet points for lists of files, functions, or changes" is generic — this applies to nearly all models.
- No guidance on the `finish` tool output format despite saying "call the finish tool immediately with a structured result."

**Recommendations:**
1. **Add an example**: Show a complete verification cycle with a hypothetical task (e.g., "Fix the login bug") and the expected output structure.
2. **Define "structured result"**: Specify what the finish output should contain — e.g., "Include: what was done, files modified, test results, and any remaining issues."
3. **Add a Kimi-specific note**: Kimi models sometimes benefit from Chinese-language reasoning — consider whether a language hint is appropriate.

---

### 2.11 `orchestrator_openai_codex.md` — OpenAI Codex Overlay

**File:** `core/prompts/orchestrator_openai_codex.md` (lines 1–21, 985 bytes)

**Summary:** Autonomous agent style. Frontend design guidance for CSS, accessibility, responsive design. Research-first mandate.

**Strengths:**
- The frontend design guidance is highly specific and actionable: semantic HTML, CSS custom properties, flexbox/grid, 150–300ms transitions, typographic hierarchy, mobile-first breakpoints.
- "Work autonomously. Make decisions and proceed." is well-aligned with Codex's agentic strengths.
- The balance between autonomy and not asking unnecessary confirmations is well-stated.

**Weaknesses:**
- The frontend guidance assumes a particular tech stack (CSS custom properties, flexbox/grid). It doesn't account for Tailwind, CSS-in-JS, or other modern approaches.
- "When multiple approaches exist, choose the best one — do not ask for confirmation on implementation details unless the task is genuinely ambiguous" — this is risky for architectural decisions where the "best" approach is subjective.

**Recommendations:**
1. **Qualify the frontend guidance**: "When working with plain CSS or CSS modules, prefer custom properties, flexbox, and grid. When the project uses Tailwind/CSS-in-JS, match the existing approach."
2. **Add an architectural decision guardrail**: "For architectural decisions (framework choice, data flow pattern, library selection), check what the project already uses before choosing. Prefer existing patterns."
3. **Add a mobile-first example**: Include a brief code example showing the mobile-first pattern.

---

### 2.12 `planner_base.md` — Planner Base Template

**File:** `core/prompts/planner_base.md` (lines 1–25, 601 bytes)

**Summary:** A template with placeholders (`MODE-PREAMBLE`, `DOMAIN-ASSIGNMENT`, `AGENT-PROFILES`, `MODE-EXTRA-SECTIONS`) that are populated at runtime from hardcoded Go constants.

**Strengths:**
- Clean separation of concerns: the template declares structure, Go code fills in content.
- The guidance section balances three key concerns: decompose research, let coders self-verify, produce concrete progress, merge related requirements.
- Placeholder naming convention is clear and grep-friendly.

**Weaknesses:**
- The template itself is skeletal — without seeing the injected content, the file is not independently comprehensible. This is by design but makes standalone review of this file difficult.
- `MODE-TAIL` is not defined — it's injected but unclear what it typically contains.
- The "Respond ONLY with a JSON object" instruction appears after `MODE-TAIL`, meaning its position relative to other instructions is runtime-dependent.

**Recommendations:**
1. **Document the template variables** in a comment at the top of the file: what each placeholder represents, what populates it, and when.
2. **Ensure `MODE-TAIL` doesn't push critical instructions out of view**: position `MODE-TAIL` before the final JSON instruction or use a named section for it.
3. **Consider making the file self-documenting** with example values in comments so reviewers can understand the full prompt without reading Go source.

---

### 2.13 `planner_replan.md` — Planner Replan Template

**File:** `core/prompts/planner_replan.md` (lines 1–28, 1254 bytes)

**Summary:** Template for revising a plan when some steps failed. Rules: preserve successful steps, add/replace only failed steps, minimal targeted changes.

**Strengths:**
- The preservation rules are explicit and correctly prioritize minimal disruption.
- Rule 6 (repeating failure pattern → broader structural change) adds a valuable escalation path.
- The JSON example uses a flat structure that's easy to parse.

**Weaknesses:**
- The template references `ORIGINAL-PLAN`, `COMPLETED-STEPS`, `FAILED-STEP`, `CURRENT-REFLECTION`, `PREVIOUS-SESSION-REFLECTIONS` as placeholders but doesn't describe their format. A planner that receives malformed data in these fields will struggle.
- "If two sequential steps failed for related reasons, consider merging them" — but merging might produce a step too large for a single executor. There's no guardrail.
- The JSON example requires a `profile` object but doesn't show all required profile fields.

**Recommendations:**
1. **Document placeholder formats**: Add a brief description of each placeholder's structure (e.g., "ORIGINAL-PLAN: the full JSON plan object from the previous attempt").
2. **Add a merging guardrail**: "If merging two steps would exceed 3 files or 4 subtasks, keep them separate and adjust their descriptions instead."
3. **Show a complete profile object** in the JSON example so the output format is unambiguous.

---

### 2.14 `planner_informed.md` — Informed Planner Template

**File:** `core/prompts/planner_informed.md` (lines 1–54, 2162 bytes)

**Summary:** Exploration-first planner. The agent first explores the codebase, then produces a plan. Includes tool priority tiers, plan quality rules, and explicit finish instructions.

**Strengths:**
- The exploration strategy is well-structured with explicit tool priority tiers.
- "Avoid trivially discoverable exploration steps" is a smart rule that prevents unnecessary plan bloat.
- "For tasks spanning multiple subsystems, creating targeted research steps for each subsystem is appropriate" acknowledges the exploration loop's limits.
- Plan quality rules connect exploration to plan output.

**Weaknesses:**
- The exploration budget is mentioned ("Budget your exploration") but no concrete budget is specified.
- The prompt says "call the finish tool" but earlier the system prompt says "Respond ONLY with a JSON object" — contradictory instructions about the output format.
- "The exploration loop cannot cover everything in its budget" — but what IS the budget? The model can't make an informed trade-off.

**Recommendations:**
1. **Specify an exploration budget**: "Spend at most 8 tool calls on exploration. If you haven't found enough after 8 calls, plan from what you have and note assumptions."
2. **Resolve the output contradiction**: Either unify around "Respond ONLY with a JSON object" or "call finish with JSON" — not both in different places.
3. **Add an "insufficient context" fallback**: "If exploration reveals the codebase is too large for the budget, decompose research into multiple plan steps rather than trying to explore everything."

---

### 2.15 `planner_default.md` — Default Planner Family Overlay

**File:** `core/prompts/planner_default.md` (lines 1–12, 547 bytes)

**Summary:** Holistic analysis, internal coherence verification, explicit assumption documentation.

**Strengths:**
- The internal coherence check questions (outputs feed into inputs? implicit dependencies? parallelization opportunities?) are concrete and effective.
- "If the task is ambiguous, note assumptions explicitly rather than guessing silently" — good epistemic hygiene.

**Weaknesses:**
- No guidance on step granularity — the base template's `planModePreamble` covers this, but this overlay doesn't reinforce it.
- The coherence check questions are the same as what the base template's guidance section already covers.

**Recommendations:**
1. **Differentiate from the base template**: Focus this overlay on what the default family specifically needs — perhaps a reminder about the DAG structure and dependency resolution.
2. **Add a "minimum viable plan" concept**: "For simple tasks (complexity 1–2), a single-step plan is acceptable."

---

### 2.16 `planner_anthropic.md` — Anthropic Planner Overlay

**File:** `core/prompts/planner_anthropic.md` (lines 1–6, 345 bytes)

**Summary:** Holistic analysis, compact descriptions, aggressive parallelization, explicit assumptions.

**Strengths:**
- "Aggressively identify parallelization opportunities" is well-targeted — Anthropic models can reason about parallelism effectively.
- Compact descriptions reduce token bloat.
- Explicit assumptions align with Anthropic model tendencies.

**Weaknesses:**
- Very short (6 lines). The overlay provides minimal differentiation from the default.
- "Identify non-obvious dependencies" is vague — what makes a dependency "non-obvious"?

**Recommendations:**
1. **Add value with concrete guidance**: "Non-obvious dependencies include: data format requirements (step B needs step A's output in JSON), timing constraints (step B needs a running service from step A), and shared mutable state."
2. **Add parallelism guardrails**: "Steps can be parallelized when they operate on disjoint sets of files. Two steps that both modify the same file CANNOT be parallelized."

---

### 2.17 `planner_openai_flagship.md` — OpenAI Flagship Planner Overlay

**File:** `core/prompts/planner_openai_flagship.md` (lines 1–12, 738 bytes)

**Summary:** Exhaustive decomposition mandate. Mandatory research step for complexity ≥ 3. Specific file paths and function names required. Verbose acceptance criteria.

**Strengths:**
- The exhaustive decomposition mandate directly counters flagship models' tendency to produce underspecified plans.
- The complexity ≥ 3 threshold for research steps is a reasonable heuristic.
- "NEVER use vague references like 'the relevant files'" is unambiguous.

**Weaknesses:**
- "Verbose What-How-Where descriptions" may produce excessively long plans that overflow planner context windows.
- The research step is always first — but for some tasks, a quick implementation step followed by research-driven refinement might be more efficient.

**Recommendations:**
1. **Add a length guardrail**: "Verbose descriptions should not exceed 200 words per step. Prioritize precision over length."
2. **Allow research later in the plan**: "The initial research step is mandatory for complexity ≥ 3, but additional targeted research steps may appear later in the plan as new unknowns are discovered."
3. **Add an example of a "good" verbose description** to illustrate the expected depth.

---

### 2.18 `planner_openai_standard.md` — OpenAI Standard Planner Overlay

**File:** `core/prompts/planner_openai_standard.md` (lines 1–10, 421 bytes)

**Summary:** 5-step linear approach: identify goal → list actions → assign roles → order steps → output JSON. Minimal plan, flat structure.

**Strengths:**
- The 5-step process is simple, learnable, and appropriate for standard models.
- "Keep the plan minimal — avoid unnecessary steps" prevents over-planning.
- Flat structure matches the capabilities of standard models well.

**Weaknesses:**
- The linear 5-step process assumes sequential planning, but the architecture supports parallel steps. This overlay doesn't mention parallelization at all.
- "No nested conditions" is overly restrictive — some steps genuinely require conditional execution.

**Recommendations:**
1. **Add parallelization awareness**: "After ordering steps, check: can any non-dependent steps run in parallel? Mark them as parallelizable."
2. **Allow conditional steps when necessary**: "Avoid conditional logic in step descriptions. If a step genuinely depends on the outcome of a previous step, note the dependency in the description rather than embedding conditionals."
3. **Add a JSON output quality check**: "Before outputting, verify: every step has a role, a description, and required tools."

---

### 2.19 `planner_gemini.md` — Gemini Planner Overlay

**File:** `core/prompts/planner_gemini.md` (lines 1–14, 672 bytes)

**Summary:** Absolute paths mandate, existing vs. greenfield workflow modes, prescriptive descriptions.

**Strengths:**
- The absolute paths requirement is correctly carried over from the orchestrator overlay — consistency matters.
- "Use prescriptive step descriptions — tell the executor exactly what to do, not what to consider" is excellent guidance that matches Gemini's capabilities.

**Weaknesses:**
- "Assign an explicit workflow mode (existing vs. greenfield) in each step description when applicable" — "when applicable" is vague. When is it NOT applicable?
- The existing/greenfield guidance is duplicated from the orchestrator overlay. If both are injected, the executor sees the same guidance twice.

**Recommendations:**
1. **Clarify workflow mode applicability**: "Assign workflow mode for every step that involves file operations. For pure analysis steps, omit the mode."
2. **Check for prompt deduplication**: Ensure the orchestrator overlay's workflow mode section doesn't appear alongside this planner overlay in the same planning prompt.
3. **Add Gemini-specific JSON guidance**: Gemini models sometimes wrap JSON in markdown fences — add a note: "Output raw JSON only, no ```json fences."

---

### 2.20 `planner_deepseek.md` — DeepSeek Planner Overlay

**File:** `core/prompts/planner_deepseek.md` (lines 1–6, 343 bytes)

**Summary:** Testable hypotheses per step, dependency and failure mode identification, explicit assumptions.

**Strengths:**
- "State the expected outcome as a testable hypothesis" is the strongest differentiator among all planner overlays. It forces the planner to think in verifiable terms.
- Succinct and focused.

**Weaknesses:**
- No example of a testable hypothesis format. A planner unfamiliar with this pattern may produce vague "hypotheses."
- "Identify dependencies and potential failure modes" is good but doesn't say what to DO with that information.

**Recommendations:**
1. **Add a hypothesis example**: "E.g., hypothesis: 'After executing this step, the auth middleware will be registered in backend/routes/api.go, all /api/* endpoints will return 401 without a valid JWT, and the existing test suite will pass.'"
2. **Add failure mode action**: "For each identified failure mode, add a corresponding acceptance criterion that would detect it."
3. **Consider adding a hypothesis verification step at the end of the plan** (optional, for complex tasks).

---

### 2.21 `planner_mistral.md` — Mistral Planner Overlay

**File:** `core/prompts/planner_mistral.md` (lines 1–6, 382 bytes)

**Summary:** Read → identify → list → order → number. No conditional logic. Focused plan.

**Strengths:**
- Perfectly simple and aligned with Mistral's tendencies.
- "No conditional logic in step descriptions — each step must be unconditionally executable" is a clean, enforceable rule.

**Weaknesses:**
- Very short. May not provide enough guidance for complex planning tasks.
- "Use numbered lists only" — but the JSON output format requires an array of objects with IDs, not numbered lists.

**Recommendations:**
1. **Align the output instruction with reality**: If the output is JSON, don't say "use numbered lists." Instead, say "each step gets a unique id field."
2. **Add complexity management**: "For complex tasks, consider splitting into two smaller plans rather than one large one."
3. **Add an "avoid ambiguity" rule**: "Step descriptions must be concrete enough that a Mistral executor (who follows flat, simple instructions) can execute them without interpretation."

---

### 2.22 `planner_kimi.md` — Kimi Planner Overlay

**File:** `core/prompts/planner_kimi.md` (lines 1–13, 487 bytes)

**Summary:** Strict What-How-Where format. Independently verifiable acceptance criteria. Explicit assumptions.

**Strengths:**
- The What-How-Where format is crystal clear and aligns well with Kimi's structured output capabilities.
- "Each criterion must be independently verifiable by the executor" is an excellent quality requirement.

**Weaknesses:**
- The What-How-Where format is also prescribed by the base template's `planModeExtraSections`. The duplication could cause confusion about which format takes precedence.
- No guidance on handling tasks where "Where" is genuinely unknown before exploration.

**Recommendations:**
1. **Reconcile with base template**: Either make this overlay the canonical definition of What-How-Where and remove the duplication from `planModeExtraSections`, or keep this overlay as a reinforcement with additional Kimi-specific notes.
2. **Add "Where: Unknown" handling**: "If the exact file location is unknown, specify the search strategy the executor should use to find it (e.g., 'Where: Locate the auth middleware using semantic_search for JWT authentication')."

---

### 2.23 `planner_openai_codex.md` — OpenAI Codex Planner Overlay

**File:** `core/prompts/planner_openai_codex.md` (lines 1–19, 731 bytes)

**Summary:** Exhaustive decomposition plus frontend awareness (component structure, CSS, responsive, transitions).

**Strengths:**
- The frontend awareness section is unique among planner overlays and valuable — it ensures frontend tasks aren't planned as purely functional exercises.
- Specific CSS and accessibility guidance carries through from the orchestrator overlay.

**Weaknesses:**
- The frontend awareness section is somewhat duplicative of `orchestrator_openai_codex.md`. If both are in context (e.g., planning a frontend task), the executor may see the same guidance twice.
- "Transition and animation polish (as a final step when appropriate)" — when is it appropriate? The planner can't tell.

**Recommendations:**
1. **Consolidate with orchestrator overlay**: Remove frontend design guidance from the planner overlay (leave it in the executor prompt). Instead, add a planning-specific note: "When planning frontend work, include a final polish step for transitions and responsive testing."
2. **Specify "when appropriate"**: "Include a transition/animation polish step when: the task involves UI component creation, user interactions (hover, click, form submission), or page transitions."

---

### 2.24 `prompt_optimize_extract.md` — Prompt Optimizer: Extract

**File:** `core/prompts/prompt_optimize_extract.md` (lines 1–12, 891 bytes)

**Summary:** Translation + keyword extraction for the first stage of the prompt optimization pipeline. Outputs raw JSON: translated prompt and 3–5 keywords.

**Strengths:**
- Clear task separation: translate (step 1) and extract keywords (step 2).
- JSON output format is unambiguous — "no markdown fencing, no explanation."
- Technical term preservation rule is explicit and correct.

**Weaknesses:**
- "Clean it up for clarity" when the prompt is already English is subjective. Different models will interpret "clean up" differently.
- The keyword extraction guidance says "focus on architecture components, patterns, function/class names" but these may be absent from a non-technical prompt. No fallback guidance.
- No examples of good vs. bad keywords.

**Recommendations:**
1. **Define "clean up"**: "Clean up: fix obvious typos, complete truncated sentences, normalize inconsistent casing of technical terms. Do NOT rephrase, shorten, or change meaning."
2. **Add keyword fallback**: "If the prompt is non-technical (e.g., a general question about programming concepts), extract domain-specific terms (e.g., 'concurrency', 'memory management') rather than forcing technical keywords."
3. **Add examples**: Show a sample input and expected output: `"fix the bug in auth"` → `{"translated": "Fix the bug in the authentication module", "keywords": ["authentication", "bug fix", "auth module"]}`.

---

### 2.25 `prompt_optimize_rewrite.md` — Prompt Optimizer: Rewrite

**File:** `core/prompts/prompt_optimize_rewrite.md` (lines 1–19, 1124 bytes)

**Summary:** Second stage of prompt optimization. Rewrites the translated prompt to be specific, actionable, structured, faithful, and concise.

**Strengths:**
- The five quality criteria (Specific, Actionable, Structured, Faithful, Concise) form a strong evaluation framework.
- "Do not add requirements that were not present or implied" is an essential faithfulness guardrail.
- Clear handling of both codebase-context-present and context-absent cases.

**Weaknesses:**
- "Reference concrete file paths, function names, type names, or patterns from the codebase context when relevant" — but "when relevant" is subjective. The model may over-reference or under-reference.
- "If the task has multiple steps, organize them logically" — but the rewrite is supposed to be a prompt for a single agent in a single execution, not a plan.
- No instruction on how much codebase context to incorporate (all of it? just the most relevant?).

**Recommendations:**
1. **Clarify "when relevant"**: "Reference codebase context snippets only when they directly help the agent understand WHERE to make changes. If a context snippet is tangentially related, omit it rather than forcing a connection."
2. **Limit context incorporation**: "Incorporate at most 3 specific references from the codebase context. More than 3 risks confusing the agent with too many locations."
3. **Add a "single-execution" constraint**: "The optimized prompt should be executable in a single agent session. If the task genuinely requires multiple phases, note this as a comment but structure the core prompt for phase 1."
4. **Add a word-count guardrail**: "Target 50–200 words for the optimized prompt. Shorter is better if all requirements are preserved."

---

### 2.26 `reflector_system.md` — Reflector Agent System Prompt

**File:** `core/prompts/reflector_system.md` (lines 1–73, 4534 bytes)

**Summary:** Self-correction analyst. Classifies failures, identifies root causes, and suggests actions (retry/replan/abort). Includes single-attempt failure classification, cross-attempt pattern analysis, and resource awareness.

**Strengths:**
- Exceptionally well-designed prompt. The decision tree for retry vs. replan vs. abort is explicit and layered.
- Single-attempt vs. multi-attempt classification is a strong architectural distinction.
- The examples at the end are concrete and illustrate the expected output format.
- "When in doubt between retry and replan, prefer replan" is a smart bias that prevents wasted retries.
- Resource awareness (max_steps, context window) adds a practical dimension.

**Weaknesses:**
- Very long (73 lines). For a reflective agent that runs after failures, this is appropriate, but some models may struggle with the full prompt.
- The failure classification types (structural, wrong_approach, recoverable, partial) overlap in their definitions. "Wrong approach" and "structural" can be hard to distinguish.
- "abort" is mentioned as an option but the guidance strongly steers away from it — this is correct but may cause the reflector to never suggest abort even when appropriate.

**Recommendations:**
1. **Add a concrete abort trigger**: "Suggest abort when: the task requires tools not available in the environment, the task is impossible given the project's constraints (e.g., modifying a read-only dependency), or 3+ replans have all failed with the same root cause."
2. **Clarify classification boundaries**: Add a decision flowchart or simple rule: "If the executor didn't reach the right files → wrong_approach. If the executor reached the right files but couldn't coordinate changes → structural."
3. **Consider adding an "unknown" root cause option**: "If root cause genuinely cannot be determined, set root_cause to 'unable to determine' and base your action suggestion on hypotheses."

---

### 2.27 `router_system.md` — Router Agent System Prompt

**File:** `core/prompts/router_system.md` (lines 1–55, 3124 bytes)

**Summary:** Request classifier. Assigns complexity (1–5), domain (code/research/mixed/general), needs_clarification flag, and matched skills.

**Strengths:**
- The complexity scale is well-defined with concrete examples at each level.
- "Simplicity bias" vs. "under-planning risk" duality is thoughtfully reasoned and provides practical heuristics.
- Domain classification includes the important "mixed" category, which many routing systems miss.
- The `needs_clarification` field has an explicit "only when genuinely ambiguous" guardrail.

**Weaknesses:**
- The complexity examples are at the boundaries — there's no example for complexity 2 or 4. This leaves a gap in the middle.
- Skill matching depends on `AVAILABLE-SKILLS` being injected — if this placeholder is empty, the section is confusing.
- "When in doubt between two domains, prefer 'mixed'" may over-classify tasks as mixed, which has implications for context compaction.

**Recommendations:**
1. **Add examples for all 5 complexity levels**: Fill the gaps at levels 2 and 4.
2. **Handle empty AVAILABLE-SKILLS**: "Available skills: [none]" should suppress the entire Skill Matching section.
3. **Refine the mixed-domain guidance**: "Prefer 'mixed' only when the task has BOTH a code component (file operations, implementation) AND a research component (web search, documentation retrieval). Tasks that only explore code are 'research', not 'mixed'."
4. **Add a "confidence" field** (optional): Allow the router to express low confidence when it's uncertain about classification.

---

### 2.28 `verification_mandate.md` — Epistemic Discipline Section

**File:** `core/prompts/verification_mandate.md` (lines 1–20, 1603 bytes)

**Summary:** Injected into all tool-enabled prompts. Mandates that agents must not fabricate facts and must verify claims through tool calls.

**Strengths:**
- Clear and unambiguous: "You MUST NOT fabricate, assume, or speculate" is the strongest possible phrasing.
- The scope is well-defined across five categories (codebase, documentation, environment, network, user intentions).
- The clarification rule ("use ask_user, don't guess") is a critical safety instruction.

**Weaknesses:**
- Purely prohibitive — it tells agents what NOT to do but offers limited guidance on what TO do when verification is costly.
- "Every claim about the external world that you rely on" — "rely on" is ambiguous. An agent might genuinely not know whether a fact is being "relied on" vs. "mentioned in passing."
- No connection to the fact memory system, which is the designated mechanism for storing verified facts.

**Recommendations:**
1. **Add a positive counterpart**: "After verifying a fact through a tool call, immediately store it with `store_fact` so subsequent steps can retrieve it without re-verification."
2. **Clarify "rely on"**: "You 'rely on' a fact when it influences your next action or your final output. Facts mentioned in passing (e.g., 'this is a Python project') should still be verified if they could affect correctness."
3. **Add a verification cost note**: "If verifying a fact would require more than 3 tool calls, note the assumption and proceed. Mark unverified assumptions explicitly in your output."

---

### 2.29 `compaction_summarize.md` — Compaction Summarizer

**File:** `core/prompts/compaction_summarize.md` (lines 1–27, 880 bytes)

**Summary:** Summarizes agent execution steps into ≤8 bullets, ~150 words. Preserves key decisions, file paths, tool outputs, errors, and current state.

**Strengths:**
- The "preserve" and "omit" lists are concrete and leave no ambiguity about what matters.
- Good/bad examples illustrate the expected quality level.
- The word and bullet limits are explicit and prevent runaway summarization.

**Weaknesses:**
- "~150 words" is approximate — some models may fixate on hitting exactly 150.
- The "preserve" list includes "build/test command outputs indicating success or failure" — but verbatim command output can be very long. No truncation guidance.
- No instruction on how to handle interleaved tool call/result pairs.

**Recommendations:**
1. **Clarify the word limit**: "Aim for 8 bullets and approximately 150 words total. Prioritize completeness over exact word count — it's better to have 10 complete bullets than 8 truncated ones."
2. **Add output truncation**: "For build/test output, include only the first error or the final summary line (e.g., 'FAILED (errors=3)' or 'BUILD SUCCESSFUL')."
3. **Add structural guidance**: "Group related actions. For example, a search-and-modify sequence should be one bullet, not two."

---

### 2.30 `judge_system.md` — Tool Safety Judge

**File:** `core/tools/prompts/judge_system.md` (lines 1–32, 1362 bytes)

**Summary:** Classifies tool calls as ALLOW (safe, auto-approve) or CONFIRM (needs user approval). Clear evaluation criteria and classification guide.

**Strengths:**
- ALLOW and CONFIRM categories are well-defined with clear examples.
- The CONFIRM category explicitly includes workspace-paths-outside, system directories, and destructive operations — good defense-in-depth.
- Response format is rigid (`VERDICT: …\nREASON: …`) making parsing reliable.

**Weaknesses:**
- "Build and test commands for the project's toolchain" is ALLOW — but what if the toolchain command is `make deploy` that pushes to production? The judge can't distinguish `make test` from `make deploy`.
- "File writes within the session workspace that align with the task" — the judge doesn't know the task well enough to evaluate alignment. A malicious or confused agent could write to the workspace in a way that's task-aligned but still dangerous.
- No guidance on what falls between ALLOW and CONFIRM — the binary classification may be too coarse.

**Recommendations:**
1. **Add a WARN category** (optional): "WARN: the call is probably safe but has edge cases. Auto-approve but log for audit." This would handle the `make deploy` scenario.
2. **Refine build command classification**: "Build/test/lint commands are ALLOW. Deployment, publish, or push commands are CONFIRM. Commands that mix both (e.g., `npm run release`) default to CONFIRM."
3. **Add task-context awareness note**: "You receive a task description. If the tool call involves files or paths not mentioned in the task description, lean toward CONFIRM."

---

### 2.31–2.34 Hardcoded Templates in `core/systemprompt.go`

**Files/lines:** `core/systemprompt.go:97–103, 126, 134–146, 149–165`

**Prompts:**
- **Workspace Context Template** (#31): Injects the workspace and temp directory paths.
- **ReAct Single-Step Completion Prompt** (#32): Reinforces finish tool usage in ReAct mode.
- **Vector Search Hints Header** (#33): Displays auto-detected relevant files.
- **Active Skills Header** (#34): Displays activated agent skills.

**Summary:** These are dynamic sections injected into the system prompt at runtime based on session state.

**Strengths:**
- Clean separation of static (embedded .md) and dynamic (Go-generated) content.
- Each section has a clear conditional gate — it only appears when relevant data exists.
- The workspace context is foundational — every executor needs to know its boundaries.

**Weaknesses:**
- The Vector Search Hints section says "Use semantic_search tool for deeper investigation" but by the time the agent reads this, semantic_search may have already been used. This is post-hoc advice.
- The Active Skills section uses `s.Metadata.AllowedTools` as a raw string — if this contains commas, it could be misinterpreted.
- The Completion Prompt uses different phrasing from `orchestrator_system.md`'s finish mandate. Minor inconsistency: the base prompt says "finish is the ONLY way to deliver results" while the completion prompt says "the system only recognizes task completion through an explicit finish tool call." Subtle difference but could confuse.

**Recommendations:**
1. **Harmonize finish tool language** across all prompts. Pick one canonical phrasing and use it consistently.
2. **Make Vector Search Hints proactive**: "Based on your query, semantic_search has identified these files. Start your investigation here, then use the files' content to guide further exploration."
3. **Sanitize AllowedTools**: Ensure the tool list is comma-separated with spaces for readability.

---

### 2.35 Role-Specific Suffixes in `core/stepconfig.go`

**File/lines:** `core/stepconfig.go:16–18`

**Summary:** Three role-specific suffixes appended to the system prompt: researcher (no file creation), coder (verify compiles), tester (no source modification).

**Strengths:**
- Concise and role-appropriate — each suffix is exactly what the role needs.
- "Do NOT create or modify project files" for researcher is an important guardrail.
- Tester's "only test infrastructure if necessary" provides flexibility without free rein.

**Weaknesses:**
- Coder's "verify your changes compile and work" is vague — "work" means what? Run tests? Manual verification?
- Researcher's "synthesize findings clearly" has no quality standard — what constitutes "clear"?
- No "executor" role suffix — executors use the default prompt without role-specific guidance.

**Recommendations:**
1. **Define "verify changes compile and work"**: "Before finishing, run the project's build command. If there are tests for the modified code, run them."
2. **Add researcher quality standard**: "Structure findings as: (1) Key discoveries, (2) Relevant file paths and functions, (3) Open questions or gaps."
3. **Consider an executor suffix**: "Your primary function is general task execution. Use all available tools as needed. Structure your approach and verify results at each step."

---

### 2.36–2.42 Planner Hardcoded Templates in `core/planner.go`

**File/lines:** `core/planner.go:33–159`

**Prompts:**
- `planModePreamble` (#36): DAG decomposition, granularity rules, MAX-STEPS limit.
- `planModeDomainAssignment` (#37): Domain selection for context compaction.
- `planModeAgentProfiles` (#38): Researcher/coder/tester/executor profiles.
- `planModeExtraSections` (#39): Step structure, output expectations, research decomposition, parallelization.
- `planModeJSONExample` (#40): JSON output example.
- `continuationModePreamble` (#41): Continuation plan preamble.
- `continuationModeJSONExample` (#42): Continuation JSON example.

**Summary:** These constants are injected into planner templates at runtime. Together they form the planning instruction backbone.

**Strengths:**
- The granularity table (complexity 1–5 → step counts) is concrete and prevents over/under-planning.
- The domain assignment section is critically important and well-reasoned — "wrong domain → wrong compaction → degraded context quality."
- Research decomposition guidance (split reading from writing, use store_fact, synthesis step) addresses a real failure mode.
- The continuation mode correctly distinguishes itself from initial planning.

**Weaknesses:**
- `planModeExtraSections` is monolithic (38 lines). It mixes step structure, output expectations, research decomposition, and parallelization into one block. Models may struggle to parse all of it.
- MAX-STEPS is a placeholder — the actual value is injected at runtime, but the planner prompt never explains what value was injected. The model can't self-limit without knowing the number.
- The `estimated_tools` field in JSON examples says "Not a constraint — the executor may use any available tool." This weakens the field's value — why include it at all?
- `continuationModePreamble` references `TERMINAL-STEPS` and `TERMINAL-STEP-IDS` as placeholders but their format is undocumented.

**Recommendations:**
1. **Split `planModeExtraSections`** into 3–4 smaller named sections with clear headings: "Step Structure", "Output Expectations by Role", "Research Task Decomposition", "Parallelization Rules."
2. **Disclose MAX-STEPS value**: Append something like "(The current limit is MAX-STEPS steps)" or inject the actual number.
3. **Either commit to `estimated_tools` or drop it**: If it's only informational, say "estimated_tools: a non-binding hint about likely tools for this step." If it's useless, remove it from the schema.
4. **Document TERMINAL-STEPS format**: Add a comment in the Go source describing the expected format.

---

### 2.43 Prompt Optimization User Message Template in `core/builder.go`

**File/lines:** `core/builder.go:478–490`

**Summary:** Constructs the user message for Step C of the prompt optimization pipeline. Contains the translated prompt and optional codebase context.

**Strengths:**
- Simple, clean template — two sections with clear headers.
- The context entries format (numbered list with file paths, line numbers, language) is parseable and useful.

**Weaknesses:**
- Context entries are truncated to 300 characters. This is reasonable for most contexts, but some function signatures with long docstrings might get cut mid-sentence.
- No instruction in the template itself about how to use the context — that responsibility falls entirely on `prompt_optimize_rewrite.md`.
- The context is always appended as a flat list — for large codebases, this could produce 20+ entries that overwhelm the rewrite model.

**Recommendations:**
1. **Improve truncation**: Truncate at sentence/word boundaries rather than hard 300-char cuts.
2. **Limit context entries**: Cap at 10 entries maximum, selecting the most relevant by similarity score.
3. **Add context relevance labeling**: "Results are sorted by relevance to your query. Items 1-3 are most relevant; items 4+ are supplementary."

---

### 2.44 Judge User Prompt Template in `core/tools/judge.go`

**File/lines:** `core/tools/judge.go` (line ~131)

**Summary:** `"Task: " + taskContext + "\n\nTool: " + toolName + "\n\nInput: " + inputStr` — simple template for the judge's user message.

**Strengths:**
- Minimal and efficient — the judge only needs task context, tool name, and input.
- The compact environment block is conditionally appended, which is appropriate (judge doesn't need full runtime details).

**Weaknesses:**
- `taskContext` comes from `TaskContextFrom(ctx)`, which may be empty. If empty, the judge evaluates without knowing what the agent is trying to do — a critical blind spot.
- The template doesn't include any instructions about how to interpret the input — that's in the system prompt, but having a brief reminder in the user message would help.
- No explicit instruction about workspace path context, even though the judge uses workspace-path fast-paths.

**Recommendations:**
1. **Guard against empty taskContext**: If taskContext is empty, append "No task context available — evaluate based on tool and input alone."
2. **Add a brief instruction**: "Evaluate whether this tool call is safe for the described task."
3. **Include workspace path**: Append "Workspace: " + workspacePath so the judge can reason about path-based safety.

---

### 2.45–2.46 Environment Block Formatters in `sdk/tools/envinfo.go`

**File/lines:** `sdk/tools/envinfo.go` (`FormatFullEnvBlock()`, `FormatCompactEnvBlock()`)

**Summary:** Generate structured environment sections for executor/planner and evaluator/judge/reflector prompts respectively.

**Strengths:**
- The dual-format approach (full for executors, compact for evaluators) is well-reasoned — evaluators don't need runtime versions.
- Environment info is collected once at startup and passed via context — efficient and consistent.
- Runtime version detection has reasonable timeouts (2s) and fails gracefully.

**Weaknesses:**
- Runtime versions are ordered: Node.js, Python, Go, .NET, Java, PHP — but this order is arbitrary and doesn't reflect actual relevance to the project. A Go project shouldn't see Node.js listed first.
- "not installed" for missing runtimes could be misleading — it might mean "not detected" due to PATH issues or non-standard installation.
- The full block doesn't include project-specific information (git branch, build tool, package manager) that would be more useful than system-level runtimes.

**Recommendations:**
1. **Order runtimes by project relevance**: Detect the project's primary language from file extensions or config files and list it first.
2. **Change "not installed" to "not detected"**: More honest about detection limitations.
3. **Add project-level info** (if available): "Project type: Go (go.mod detected), Build tool: go build, Package manager: go mod."

---

## 3. Overall Patterns & Priority Improvements

### 3.1 Patterns Observed

**Strengths across the codebase:**
1. **Family-specific adaptation works well.** The orchestrator and planner family overlays are targeted and collectively cover model-specific failure modes (Claude's verbosity, Gemini's path handling, DeepSeek's hypothesis-driven reasoning, Mistral's simplicity needs).
2. **Separation of concerns between static .md and dynamic Go.** Embedded files carry stable instructions; Go code injects session-specific context (workspace, environment, skills, vector hints). This is architecturally sound.
3. **The prompt optimization pipeline is a differentiator.** Extracting keywords → searching codebase → rewriting prompts with context is a sophisticated approach not commonly seen in coding agents.
4. **Verification mandate is universal.** Injecting epistemic discipline into every prompt is a strong design choice that prevents a major failure mode.
5. **The reflector is exceptionally well-designed.** Its multi-layered classification (single-attempt vs. cross-attempt, structural vs. recoverable) shows deep thought about failure analysis.

**Recurring weaknesses:**
1. **Length inflation.** The orchestration system prompt + family overlay + verification mandate + workspace context + environment block + vector hints + active skills = 9–10KB. This taxes model attention, especially for smaller/standard models.
2. **Redundancy between orchestrator and planner overlays.** The Gemini overlays for both agents repeat the absolute-paths mandate. The Anthropic overlays repeat "compact descriptions." The OpenAI Codex overlays repeat frontend guidance.
3. **Duplication between Go constants and .md files.** `planModeExtraSections` (Go) and `planner_base.md` (.md) both reference What-How-Where. `planModePreamble` overlaps with `planner_informed.md`'s exploration strategy. This dual-source-of-truth pattern invites drift.
4. **Vague qualifiers.** "When relevant," "when applicable," "when appropriate," "complex tasks," "critical commands" appear across many prompts without concrete definitions.
5. **Missing examples.** Several prompts describe a process without illustrating it (Kimi verification cycle, DeepSeek testable hypotheses, prompt optimizer keyword extraction). Examples anchor instructions.
6. **Finish tool language inconsistency.** The base system prompt, plan context, and single-step completion prompt each phrase the finish mandate slightly differently.
7. **No explicit token budget awareness.** Only the search efficiency section addresses budget, and it's limited to search calls. No prompt addresses the total context window budget.

### 3.2 Priority Improvements (Ordered by Impact)

| Priority | Recommendation | Affected Prompts | Effort |
|----------|---------------|-----------------|--------|
| **P0** | Add a "Minimum Viable Prompt" variant (40 lines) for tight token budgets | `orchestrator_system.md` | Medium |
| **P0** | Consolidate Gemini's absolute-paths rule into ONE place (orchestrator only) | `orchestrator_gemini.md`, `planner_gemini.md` | Low |
| **P1** | Add concrete examples to: Kimi verification cycle, DeepSeek hypotheses, prompt optimizer keywords, planner JSON output | `orchestrator_kimi.md`, `planner_deepseek.md`, `prompt_optimize_extract.md`, `planner_base.md` | Medium |
| **P1** | Harmonize finish tool language across all prompts | `orchestrator_system.md`, `orchestrator_plan_context.md`, `systemprompt.go:126` | Low |
| **P1** | Define all vague qualifiers with concrete criteria | ~12 prompts | High |
| **P2** | Split `planModeExtraSections` into smaller named sections | `core/planner.go:82–119` | Low |
| **P2** | Consolidate frontend guidance (remove from planner Codex overlay, keep in orchestrator) | `orchestrator_openai_codex.md`, `planner_openai_codex.md` | Low |
| **P2** | Add project-level info to EnvInfo (primary language, build tool) | `sdk/tools/envinfo.go` | Medium |
| **P2** | Add WARN verdict category to tool judge | `judge_system.md`, `core/tools/judge.go` | Medium |
| **P3** | Add abort trigger examples to reflector | `reflector_system.md` | Low |
| **P3** | Add confidence field to router output | `router_system.md` | Low |
| **P3** | Document all template placeholders in planner .md files | `planner_base.md`, `planner_replan.md` | Low |

---

*End of Report*
