# Planner System

<cite>
**Referenced Files in This Document**
- [planner.go](file://core/planner.go)
- [router.go](file://core/router.go)
- [orchestrator.go](file://core/orchestrator.go)
- [prompts.go](file://core/prompts/prompts.go)
- [planner_base.md](file://core/prompts/planner_base.md)
- [planner_informed.md](file://core/prompts/planner_informed.md)
- [planner_default.md](file://core/prompts/planner_default.md)
- [router_system.md](file://core/prompts/router_system.md)
- [router_instructions.md](file://core/prompts/router_instructions.md)
- [types.go](file://core/types.go)
- [builder.go](file://core/builder.go)
- [systemprompt.go](file://core/systemprompt.go)
- [planner_test.go](file://core/planner_test.go)
- [reflector.go](file://core/reflector.go)
- [AGENTS.md](file://AGENTS.md)
</cite>

## Update Summary
**Changes Made**
- Updated planner system documentation to reflect simplified tool priority guidance
- Removed explicit tool priority tier instructions from planner templates
- Consolidated tool recommendations into broader guidance patterns
- Updated prompt engineering system documentation to reflect streamlined approach
- Enhanced project-specific instructions integration documentation

## Table of Contents
1. [Introduction](#introduction)
2. [Project Structure](#project-structure)
3. [Core Components](#core-components)
4. [Architecture Overview](#architecture-overview)
5. [Detailed Component Analysis](#detailed-component-analysis)
6. [Project-Specific Instructions Integration](#project-specific-instructions-integration)
7. [Dependency Analysis](#dependency-analysis)
8. [Performance Considerations](#performance-considerations)
9. [Troubleshooting Guide](#troubleshooting-guide)
10. [Conclusion](#conclusion)

## Introduction
This document explains the C0WRK planner system and its role in generating execution plans for AI tasks. The planner supports:
- Synthetic plans for simple tasks (minimal overhead)
- Full LLM-generated plans for complex scenarios
- Adaptive strategy selection based on domain and tool availability
- Integration with the Router for domain classification and tool availability assessment
- PlanContinuation for follow-up requests after task completion
- Prompt engineering with domain-specific templates
- Relationship between planner complexity scores and execution strategies
- **Updated**: Simplified tool guidance system with consolidated recommendations

## Project Structure
The planner system resides in the core package and integrates with the broader orchestration framework. Key files include:
- Planner implementation and prompt engineering
- Router for domain classification and complexity scoring
- Orchestrator coordinating routing, planning, and execution
- Prompt templates for planner and router
- Builder wiring components together
- Types and system prompt utilities
- **Updated**: Streamlined planner templates with simplified tool guidance

```mermaid
graph TB
subgraph "Core"
Planner["Planner (planner.go)"]
Router["Router (router.go)"]
Orchestrator["Orchestrator (orchestrator.go)"]
Prompts["Prompts (prompts.go)"]
Types["Types (types.go)"]
Builder["Builder (builder.go)"]
SysPrompt["System Prompt (systemprompt.go)"]
Reflector["Reflector (reflector.go)"]
AgentsMD["AGENTS.md Manager"]
end
subgraph "Prompts"
PB["planner_base.md"]
PI["planner_informed.md"]
PD["planner_default.md"]
RS["router_system.md"]
RI["router_instructions.md"]
end
Orchestrator --> Router
Orchestrator --> Planner
Planner --> Prompts
Router --> Prompts
Builder --> Orchestrator
Builder --> Planner
Builder --> Router
Builder --> SysPrompt
Orchestrator --> Reflector
Planner --> Types
Router --> Types
Orchestrator --> Types
AgentsMD --> Planner
AgentsMD --> Orchestrator
```

**Diagram sources**
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [router.go:22-114](file://core/router.go#L22-L114)
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [prompts.go:6-168](file://core/prompts/prompts.go#L6-L168)
- [planner_base.md:1-25](file://core/prompts/planner_base.md#L1-L25)
- [planner_informed.md:1-54](file://core/prompts/planner_informed.md#L1-L54)
- [planner_default.md:1-12](file://core/prompts/planner_default.md#L1-L12)
- [router_system.md:1-42](file://core/prompts/router_system.md#L1-L42)
- [router_instructions.md:1-6](file://core/prompts/router_instructions.md#L1-L6)
- [builder.go:423-471](file://core/builder.go#L423-L471)
- [systemprompt.go:44-100](file://core/systemprompt.go#L44-L100)
- [reflector.go:24-80](file://core/reflector.go#L24-L80)

**Section sources**
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [router.go:22-114](file://core/router.go#L22-L114)
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [prompts.go:6-168](file://core/prompts/prompts.go#L6-L168)
- [builder.go:423-471](file://core/builder.go#L423-L471)
- [systemprompt.go:44-100](file://core/systemprompt.go#L44-L100)
- [reflector.go:24-80](file://core/reflector.go#L24-L80)

## Core Components
- Planner: Generates DAG execution plans using either synthetic or informed exploration strategies, depending on domain and tool availability. It builds domain-aware system prompts and parses structured JSON plans. **Updated**: Now uses consolidated tool guidance instead of explicit priority tiers.
- Router: Classifies user requests by domain and complexity, enabling adaptive planning and execution strategies.
- Orchestrator: Coordinates routing, planning, and execution, integrating planner decisions with the broader orchestration engine. It supports synthetic plan thresholds and plan continuation workflows. **Enhanced**: Automatically discovers and injects AGENTS.md content from workspace root.
- Prompt Templates: Domain-specific and model-family-specific templates guide planner and router behavior with simplified tool guidance.
- Builder: Wires the planner, router, and orchestrator with shared tool registries, context factories, and model registries.
- **Updated**: Streamlined planner templates with consolidated tool recommendations replacing explicit priority tier instructions.

**Section sources**
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [router.go:22-114](file://core/router.go#L22-L114)
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [prompts.go:6-168](file://core/prompts/prompts.go#L6-L168)
- [builder.go:423-471](file://core/builder.go#L423-L471)
- [systemprompt.go:44-100](file://core/systemprompt.go#L44-L100)

## Architecture Overview
The planner participates in a multi-agent pipeline with enhanced project context awareness:
- Router classifies the request and provides domain and complexity.
- Orchestrator decides between synthetic and full planning based on the synthetic plan threshold.
- **Enhanced**: Orchestrator automatically discovers AGENTS.md from workspace root and injects project-specific instructions.
- Planner generates a plan using domain-aware prompts, tool availability, and project context with simplified guidance.
- Orchestrator executes the plan and manages continuations and replanning.

```mermaid
sequenceDiagram
participant User as "User"
participant Orchestrator as "Orchestrator"
participant Router as "Router"
participant Planner as "Planner"
participant Executor as "Executor"
User->>Orchestrator : "Message"
Orchestrator->>Orchestrator : "Inject AGENTS.md context"
Orchestrator->>Router : "Route(userMessage, availableTools, history)"
Router-->>Orchestrator : "RoutingDecision(domain, complexity, needs_clarification)"
Orchestrator->>Orchestrator : "Select synthetic vs full plan"
alt "Synthetic plan"
Orchestrator->>Planner : "CreateSyntheticPlan(task, domain)"
Planner->>Planner : "Append AGENTS.md instructions"
Planner-->>Orchestrator : "Plan{Steps : [{ID : step_1,...}]}"
else "Full plan"
Orchestrator->>Planner : "Plan(ctx, task, availableTools, reflections)"
Planner->>Planner : "Append AGENTS.md instructions"
Planner->>Executor : "Run exploration (ReAct)"
Executor-->>Planner : "Finish tool with plan JSON"
Planner-->>Orchestrator : "Plan{Steps : [...]}"
end
Orchestrator->>Executor : "Resume execution with Plan"
Executor-->>Orchestrator : "ExecutionResult"
Orchestrator-->>User : "Output + Plan + Reflections"
```

**Diagram sources**
- [router.go:46-114](file://core/router.go#L46-L114)
- [orchestrator.go:436-476](file://core/orchestrator.go#L436-L476)
- [planner.go:257-421](file://core/planner.go#L257-L421)
- [builder.go:423-471](file://core/builder.go#L423-L471)

## Detailed Component Analysis

### Planner
The Planner generates DAG execution plans with two strategies:
- Direct planning for "general" domain or when no exploration tools are available.
- Informed exploration planning using a bounded ReAct loop with a dedicated executor.

Key capabilities:
- Domain-aware prompt construction using planner templates and model-family adaptations.
- Tool filtering for exploration (codebase-memory MCP tools and filesystem tools).
- Plan parsing supporting JSON in code blocks and structured fields.
- Replan and PlanContinuation for failure recovery and follow-ups.
- **Updated**: Simplified tool guidance system with consolidated recommendations instead of explicit priority tiers.

```mermaid
classDiagram
class Planner {
+Plan(ctx, task, availableTools, reflections) *Plan
+Replan(ctx, originalPlan, completedSteps, failedStep, reflection, sessionReflections) *Plan
+PlanContinuation(ctx, originalRequest, existingPlan, completedSteps, newMessage, availableTools) *Plan
+CreateSyntheticPlan(task, domain) *Plan
-planDirect(...)
-planWithExploration(...)
-buildPlanSystemPrompt(...)
-buildInformedPlanSystemPrompt(...)
-buildReplanSystemPrompt(...)
-buildContinuationSystemPrompt(...)
-getPlannerTools() []ToolDescriptor
-hasCodebaseMemoryTools() bool
+formatAgentsMD(ctx) string
}
```

**Diagram sources**
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [planner.go:534-606](file://core/planner.go#L534-L606)
- [planner.go:755-806](file://core/planner.go#L755-L806)
- [planner.go:885-903](file://core/planner.go#L885-L903)

**Section sources**
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [planner.go:534-606](file://core/planner.go#L534-L606)
- [planner.go:755-806](file://core/planner.go#L755-L806)
- [planner.go:885-903](file://core/planner.go#L885-L903)
- [planner_test.go:800-964](file://core/planner_test.go#L800-L964)
- [planner_test.go:1387-1438](file://core/planner_test.go#L1387-L1438)

### Router
The Router classifies user requests by domain and complexity, guiding the planner's strategy:
- Uses a system prompt and instruction template to guide classification.
- Validates and clamps complexity to [1, 5].
- Applies domain-based compaction strategy rules.

```mermaid
flowchart TD
Start(["Route(userMessage, availableTools, history)"]) --> BuildPrompt["Build system prompt with available tools"]
BuildPrompt --> Messages["Assemble messages (system + recent history + user)"]
Messages --> CallLLM["Call LLM"]
CallLLM --> ExtractJSON["Extract JSON (code block or raw)"]
ExtractJSON --> Unmarshal["Unmarshal RoutingDecision"]
Unmarshal --> Validate["Validate and clamp domain/complexity"]
Validate --> Decision["RoutingDecision{domain, complexity, needs_clarification}"]
Decision --> End(["Return decision"])
```

**Diagram sources**
- [router.go:46-114](file://core/router.go#L46-L114)
- [router.go:137-154](file://core/router.go#L137-L154)
- [router_system.md:1-42](file://core/prompts/router_system.md#L1-L42)
- [router_instructions.md:1-6](file://core/prompts/router_instructions.md#L1-L6)

**Section sources**
- [router.go:22-114](file://core/router.go#L22-L114)
- [router.go:137-154](file://core/router.go#L137-L154)
- [router_system.md:1-42](file://core/prompts/router_system.md#L1-L42)
- [router_instructions.md:1-6](file://core/prompts/router_instructions.md#L1-L6)

### Orchestrator Integration and Adaptive Strategy
The Orchestrator integrates the Router and Planner:
- Routes incoming messages and emits routing decisions.
- Chooses synthetic vs full planning based on the synthetic plan threshold.
- Supports PlanContinuation for follow-up requests after task completion.
- Merges continuation plans with existing plans and resumes execution.
- **Enhanced**: Automatically discovers AGENTS.md from workspace root and injects project-specific instructions.

```mermaid
sequenceDiagram
participant Orchestrator as "Orchestrator"
participant Router as "Router"
participant Planner as "Planner"
participant BB as "Blackboard"
Orchestrator->>Orchestrator : "Inject AGENTS.md context"
Orchestrator->>Router : "Route(message, availableTools, history)"
Router-->>Orchestrator : "RoutingDecision"
Orchestrator->>BB : "SetOriginalRequest(message)"
Orchestrator->>Planner : "CreateSyntheticPlan or Plan(...)"
Planner-->>Orchestrator : "Plan"
Orchestrator->>BB : "SetPlan(plan)"
Orchestrator->>Planner : "PlanContinuation(...) for continuations"
Planner-->>Orchestrator : "Continuation Plan"
Orchestrator->>BB : "Merge and SetPlan(merged)"
Orchestrator-->>Orchestrator : "Resume execution"
```

**Diagram sources**
- [orchestrator.go:406-559](file://core/orchestrator.go#L406-L559)
- [router.go:46-114](file://core/router.go#L46-L114)
- [planner.go:575-606](file://core/planner.go#L575-L606)

**Section sources**
- [orchestrator.go:406-559](file://core/orchestrator.go#L406-L559)
- [types.go:239-244](file://core/types.go#L239-L244)

### Prompt Engineering System
The planner leverages domain-specific and model-family-specific prompts:
- Base planner prompt defines structure and expectations with consolidated guidance.
- Informed planner prompt guides exploration with simplified tool recommendations.
- Router system and instruction prompts guide classification.
- Family-specific overlays adapt behavior per model family.
- **Updated**: Planner templates now use consolidated tool guidance instead of explicit priority tiers.
- **Enhanced**: AGENTS.md project instructions are automatically appended to all planner prompts when available.

```mermaid
graph LR
PB["planner_base.md"] --> Planner["Planner"]
PI["planner_informed.md"] --> Planner
PD["planner_default.md"] --> Planner
RS["router_system.md"] --> Router["Router"]
RI["router_instructions.md"] --> Router
FP["FamilyPrompt('planner', family)"] --> Planner
FR["FamilyPrompt('orchestrator', family)"] --> Orchestrator["Orchestrator"]
AMD["AGENTS.md Content"] --> Planner
AMD --> Orchestrator
```

**Diagram sources**
- [prompts.go:6-168](file://core/prompts/prompts.go#L6-L168)
- [planner_base.md:1-25](file://core/prompts/planner_base.md#L1-L25)
- [planner_informed.md:1-54](file://core/prompts/planner_informed.md#L1-L54)
- [planner_default.md:1-12](file://core/prompts/planner_default.md#L1-L12)
- [router_system.md:1-42](file://core/prompts/router_system.md#L1-L42)
- [router_instructions.md:1-6](file://core/prompts/router_instructions.md#L1-L6)

**Section sources**
- [prompts.go:6-168](file://core/prompts/prompts.go#L6-L168)
- [planner_base.md:1-25](file://core/prompts/planner_base.md#L1-L25)
- [planner_informed.md:1-54](file://core/prompts/planner_informed.md#L1-L54)
- [planner_default.md:1-12](file://core/prompts/planner_default.md#L1-L12)
- [router_system.md:1-42](file://core/prompts/router_system.md#L1-L42)
- [router_instructions.md:1-6](file://core/prompts/router_instructions.md#L1-L6)

### PlanContinuation Mechanism
PlanContinuation enables follow-up requests after task completion:
- Builds a continuation prompt including completed plan summaries and terminal steps.
- Enforces step ID prefixes and dependency on terminal steps.
- Merges continuation steps into the existing plan and resumes execution.
- **Enhanced**: Automatically includes AGENTS.md project instructions in continuation prompts.

```mermaid
sequenceDiagram
participant Orchestrator as "Orchestrator"
participant Planner as "Planner"
participant BB as "Blackboard"
Orchestrator->>BB : "GetPlan()"
Orchestrator->>Planner : "PlanContinuation(originalRequest, existingPlan, completedSteps, newMessage, availableTools)"
Planner->>Planner : "Append AGENTS.md instructions"
Planner-->>Orchestrator : "Continuation Plan"
Orchestrator->>BB : "Merge existingPlan + continuationPlan"
Orchestrator->>Orchestrator : "Resume execution"
```

**Diagram sources**
- [orchestrator.go:517-544](file://core/orchestrator.go#L517-L544)
- [planner.go:575-606](file://core/planner.go#L575-L606)

**Section sources**
- [orchestrator.go:517-544](file://core/orchestrator.go#L517-L544)
- [planner.go:575-606](file://core/planner.go#L575-L606)
- [planner_test.go:1387-1438](file://core/planner_test.go#L1387-L1438)

### Synthetic Plan Threshold and Complexity-Based Decision Making
The Orchestrator uses a synthetic plan threshold to decide between synthetic and full planning:
- If complexity ≤ threshold, use CreateSyntheticPlan for minimal overhead.
- Otherwise, route through the planner's full planning path.

```mermaid
flowchart TD
Start(["Incoming Message"]) --> Route["Router.Route(...)"]
Route --> Decision{"complexity <= threshold?"}
Decision --> |Yes| Synthetic["CreateSyntheticPlan(task, domain)"]
Decision --> |No| FullPlan["Planner.Plan(ctx, task, availableTools, reflections)"]
Synthetic --> Execute["Resume execution"]
FullPlan --> Execute
Execute --> End(["Return result"])
```

**Diagram sources**
- [orchestrator.go:444-457](file://core/orchestrator.go#L444-L457)
- [planner.go:949-967](file://core/planner.go#L949-L967)

**Section sources**
- [orchestrator.go:39-43](file://core/orchestrator.go#L39-L43)
- [orchestrator.go:444-457](file://core/orchestrator.go#L444-L457)
- [planner.go:949-967](file://core/planner.go#L949-L967)

### Relationship Between Planner Complexity Scores and Execution Strategies
- Router assigns complexity 1–5 and domain ("code", "research", "general", "mixed").
- Orchestrator uses complexity to select synthetic vs full planning.
- Domain influences compaction strategy and tool prioritization in prompts.
- Replan and reflection integrate feedback loops to refine execution strategies.
- **Enhanced**: Project-specific instructions from AGENTS.md influence planning decisions and step descriptions.
- **Updated**: Simplified tool guidance system reduces complexity while maintaining effectiveness.

**Section sources**
- [router.go:137-154](file://core/router.go#L137-L154)
- [orchestrator.go:444-457](file://core/orchestrator.go#L444-L457)
- [reflector.go:42-80](file://core/reflector.go#L42-L80)

## Project-Specific Instructions Integration

### AGENTS.md Management System
The C0WRK system now supports project-specific instructions through AGENTS.md files located in the workspace root. This functionality enhances consistency and ensures all planning contexts adhere to project standards.

#### Automatic Discovery and Injection
- **Workspace Detection**: The Orchestrator automatically scans the workspace root for AGENTS.md files.
- **Context Injection**: When found, AGENTS.md content is injected into the planning context using `WithAgentsMD`.
- **Priority Handling**: AGENTS.md is always prepended as the first hint in vector search results to ensure prominence.

#### Context Management
The system uses a dedicated context key system for AGENTS.md management:
- **Context Keys**: `agentsMDKeyType` provides thread-safe storage for project instructions.
- **Access Functions**: `WithAgentsMD` and `AgentsMDFromContext` enable seamless context manipulation.
- **Nil Safety**: Functions gracefully handle missing AGENTS.md content without breaking execution.

#### Prompt Integration
AGENTS.md content is automatically included in all planner prompts:
- **Initial Planning**: Project instructions are appended to base planner prompts.
- **Informed Planning**: Additional instructions guide exploration and tool usage.
- **Replanning**: Failure recovery maintains project context consistency.
- **Continuation Planning**: Follow-up requests incorporate project standards.

#### Format and Structure
The system formats AGENTS.md content for optimal LLM processing:
- **Structured Presentation**: Content is wrapped in a dedicated section with clear headers.
- **Instruction Emphasis**: Prompts include directives for strict adherence to project rules.
- **Contradiction Handling**: Guidance for resolving conflicts between project rules and codebase context.

```mermaid
flowchart TD
Start(["Workspace Scan"]) --> CheckFile["Check for AGENTS.md in workspace root"]
CheckFile --> Found{"File Found?"}
Found --> |Yes| ReadFile["Read AGENTS.md content"]
ReadFile --> InjectContext["Inject via WithAgentsMD"]
InjectContext --> PrependHint["Prepend as first vector hint"]
PrependHint --> Ready["Context Ready"]
Found --> |No| Skip["Skip injection"]
Skip --> Ready
Ready --> AllPrompts["All planner prompts include AGENTS.md"]
```

**Diagram sources**
- [systemprompt.go:44-66](file://core/systemprompt.go#L44-L66)
- [orchestrator.go:305-330](file://core/orchestrator.go#L305-L330)
- [planner.go:885-903](file://core/planner.go#L885-L903)

**Section sources**
- [systemprompt.go:44-66](file://core/systemprompt.go#L44-L66)
- [orchestrator.go:305-330](file://core/orchestrator.go#L305-L330)
- [planner.go:885-903](file://core/planner.go#L885-L903)
- [planner_test.go:1460-1604](file://core/planner_test.go#L1460-L1604)

## Dependency Analysis
The planner system exhibits clear separation of concerns with enhanced project context management:
- Planner depends on prompt templates, tool registries, model metadata, and AGENTS.md context.
- Router depends on prompt templates and tool availability.
- Orchestrator orchestrates both and integrates with the SDK orchestration engine, managing AGENTS.md discovery.
- Builder wires components and provides shared registries and context factories.
- **Enhanced**: AGENTS.md manager provides centralized context management across all components.
- **Updated**: Simplified tool guidance system reduces template complexity while maintaining functionality.

```mermaid
graph TB
Builder["Builder"] --> Orchestrator["Orchestrator"]
Builder --> Planner["Planner"]
Builder --> Router["Router"]
Orchestrator --> Planner
Orchestrator --> Router
Planner --> Prompts["Prompts"]
Router --> Prompts
Orchestrator --> Types["Types"]
Planner --> Types
Router --> Types
Orchestrator --> SystemPrompt["System Prompt"]
Orchestrator --> AgentsMD["AGENTS.md Manager"]
Planner --> AgentsMD
AgentsMD --> Context["Context Management"]
```

**Diagram sources**
- [builder.go:423-471](file://core/builder.go#L423-L471)
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [router.go:22-114](file://core/router.go#L22-L114)
- [systemprompt.go:44-100](file://core/systemprompt.go#L44-L100)

**Section sources**
- [builder.go:423-471](file://core/builder.go#L423-L471)
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [planner.go:168-421](file://core/planner.go#L168-L421)
- [router.go:22-114](file://core/router.go#L22-L114)
- [systemprompt.go:44-100](file://core/systemprompt.go#L44-L100)

## Performance Considerations
- Synthetic plans reduce LLM calls for simple tasks, lowering cost and latency.
- Informed exploration uses bounded ReAct loops and circuit breakers to prevent runaway token usage.
- Tool result budgets and context window management mitigate memory pressure.
- Auto-RAG hints and environment context improve plan quality without excessive prompting.
- **Enhanced**: AGENTS.md injection adds minimal overhead while significantly improving plan consistency and adherence to project standards.
- **Updated**: Simplified tool guidance system reduces prompt complexity and improves processing efficiency.

## Troubleshooting Guide
Common issues and resolutions:
- Router JSON parsing failures: The Router retries with a repair prompt when JSON is malformed.
- Exploration executor failures: The Planner falls back to direct planning when exploration fails.
- Context cancellation propagation: Cancellation errors are preserved through the planning process.
- Plan parsing errors: The Planner falls back to direct planning when exploration output cannot be parsed.
- **Enhanced**: AGENTS.md context issues: Missing or malformed AGENTS.md content is gracefully handled without affecting planning functionality.
- **Updated**: Tool guidance confusion: Simplified guidance eliminates priority tier confusion while maintaining effective tool utilization.

**Section sources**
- [router.go:89-109](file://core/router.go#L89-L109)
- [planner.go:393-416](file://core/planner.go#L393-L416)
- [planner_test.go:1201-1289](file://core/planner_test.go#L1201-L1289)

## Conclusion
The C0WRK planner system provides a robust, adaptive approach to plan generation with enhanced project context awareness:
- It intelligently selects between synthetic and full planning based on domain and complexity.
- It integrates tightly with the Router and Orchestrator to support seamless execution and continuations.
- Its prompt engineering system ensures domain-specific, model-family-adapted behavior with simplified tool guidance.
- **Enhanced**: Project-specific instructions from AGENTS.md provide consistent context across all planning scenarios.
- **Updated**: Streamlined tool guidance system reduces complexity while maintaining effectiveness.
- It offers resilience through fallbacks, replanning, and reflection-driven improvements.
- **New**: Centralized AGENTS.md management ensures all planning contexts maintain project standards and guidelines.