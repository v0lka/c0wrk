# Agent Executor

<cite>
**Referenced Files in This Document**
- [executor.go](file://sdk/agent/executor.go)
- [subagent.go](file://sdk/agent/subagent.go)
- [finish.go](file://sdk/agent/finish.go)
- [types.go](file://sdk/agent/types.go)
- [events.go](file://sdk/agent/events.go)
- [workspace.go](file://sdk/agent/workspace.go)
- [toolformat.go](file://sdk/agent/toolformat.go)
- [orchestrator.go](file://sdk/orchestration/orchestrator.go)
- [core_orchestrator.go](file://core/orchestrator.go)
- [context.go](file://sdk/memory/context.go)
- [provider.go](file://sdk/llm/provider.go)
- [registry.go](file://sdk/tools/registry.go)
- [emitter.go](file://backend/session/emitter.go)
</cite>

## Table of Contents
1. [Introduction](#introduction)
2. [Project Structure](#project-structure)
3. [Core Components](#core-components)
4. [Architecture Overview](#architecture-overview)
5. [Detailed Component Analysis](#detailed-component-analysis)
6. [Dependency Analysis](#dependency-analysis)
7. [Performance Considerations](#performance-considerations)
8. [Troubleshooting Guide](#troubleshooting-guide)
9. [Conclusion](#conclusion)
10. [Appendices](#appendices)

## Introduction
This document explains the C0WRK agent executor system that powers the ReAct loop (Reasoning and Acting). It covers:
- The ReAct loop implementation with explicit reasoning and acting phases
- Circuit breaker protections against repeated tool misuse, truncation, parse errors, and fruitless results
- Step-by-step execution monitoring and event emission
- Integration with the tool registry, LLM providers, and context management
- Subagent system enabling parallel plan execution
- Finish detection and task completion semantics
- Error handling, retry logic, and budget management
- Coordination with the orchestrator’s planning and routing decisions

## Project Structure
The executor resides in the SDK agent package and integrates with the orchestration engine, memory, tools, and LLM layers. The orchestrator coordinates planning and routing, while the executor runs the ReAct loop per step.

```mermaid
graph TB
subgraph "SDK Agent"
EX["Executor<br/>ReAct loop"]
SA["SubAgent<br/>parallel execution"]
FIN["FinishTool<br/>completion signal"]
CTX["ContextManager<br/>token budget & compaction"]
EVT["AgentEvents<br/>execution events"]
end
subgraph "Orchestration Engine"
ORCH["Orchestrator<br/>Plan&Execute loop"]
BB["Blackboard<br/>state & persistence"]
end
subgraph "LLM Layer"
LLM["LLMCaller<br/>provider abstraction"]
PRV["Provider<br/>multi-provider support"]
end
subgraph "Tools Layer"
REG["ToolRegistry<br/>tool discovery & execution"]
end
ORCH --> EX
ORCH --> SA
ORCH --> CTX
ORCH --> EVT
ORCH --> LLM
ORCH --> REG
EX --> LLM
EX --> REG
EX --> CTX
EX --> EVT
SA --> EX
FIN --> REG
LLM --> PRV
```

**Diagram sources**
- [executor.go:49-95](file://sdk/agent/executor.go#L49-L95)
- [subagent.go:11-23](file://sdk/agent/subagent.go#L11-L23)
- [finish.go:12-28](file://sdk/agent/finish.go#L12-L28)
- [context.go:27-43](file://sdk/memory/context.go#L27-L43)
- [events.go:5-26](file://sdk/agent/events.go#L5-L26)
- [orchestrator.go:20-47](file://sdk/orchestration/orchestrator.go#L20-L47)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)
- [registry.go:11-17](file://sdk/tools/registry.go#L11-L17)

**Section sources**
- [executor.go:49-116](file://sdk/agent/executor.go#L49-L116)
- [orchestrator.go:20-47](file://sdk/orchestration/orchestrator.go#L20-L47)

## Core Components
- Executor: Implements the ReAct loop, manages tool calls, budgets, and circuit breakers.
- SubAgent: Wraps an Executor to run steps in parallel.
- FinishTool: Special tool signaling task completion.
- ContextManager: Tracks token usage, enforces budgets, and compacts context.
- AgentEvents: Emits lifecycle events for UI and observability.
- Orchestrator: Coordinates planning, routing, and step execution; wires the executor into the plan DAG.

**Section sources**
- [executor.go:49-116](file://sdk/agent/executor.go#L49-L116)
- [subagent.go:11-23](file://sdk/agent/subagent.go#L11-L23)
- [finish.go:12-28](file://sdk/agent/finish.go#L12-L28)
- [context.go:27-43](file://sdk/memory/context.go#L27-L43)
- [events.go:5-26](file://sdk/agent/events.go#L5-L26)
- [orchestrator.go:20-47](file://sdk/orchestration/orchestrator.go#L20-L47)

## Architecture Overview
The orchestrator builds a plan and dispatches steps to executors. Each executor maintains its own ReAct loop with integrated budgeting and safety checks. Parallelism is achieved via subagents that run executors concurrently.

```mermaid
sequenceDiagram
participant User as "User"
participant Orchestrator as "Core Orchestrator"
participant Engine as "SDK Orchestrator"
participant Step as "Executor(SubAgent)"
participant LLM as "LLMCaller"
participant Tools as "ToolRegistry"
participant CM as "ContextManager"
User->>Orchestrator : "HandleMessage(...)"
Orchestrator->>Engine : "Resume(bb)"
Engine->>Engine : "Find ready steps"
Engine->>Step : "Create Executor + ContextManager"
Step->>CM : "BuildPrompt()"
Step->>LLM : "Call(ChatRequest{Messages, Tools, MaxTokens})"
LLM-->>Step : "ChatResponse{Message, Reasoning, StopReason, Usage}"
Step->>Step : "Parse response, detect finish"
alt Tool calls present
Step->>Tools : "Execute(toolName, input)"
Tools-->>Step : "ToolResult{Content, IsError}"
Step->>CM : "AddStep(Thought + Action + Observation)"
else No tool calls
Step->>Step : "Implicit finish or nudge"
end
Step-->>Engine : "ExecutorResult{Output, Steps, Finished}"
Engine-->>Orchestrator : "CompletedStep"
Orchestrator-->>User : "Final output"
```

**Diagram sources**
- [core_orchestrator.go:344-598](file://core/orchestrator.go#L344-L598)
- [orchestrator.go:348-752](file://sdk/orchestration/orchestrator.go#L348-L752)
- [executor.go:202-773](file://sdk/agent/executor.go#L202-L773)
- [context.go:167-200](file://sdk/memory/context.go#L167-L200)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)
- [registry.go:91-100](file://sdk/tools/registry.go#L91-L100)

## Detailed Component Analysis

### Executor: ReAct Loop with Safety and Budgeting
The executor runs a ReAct loop with explicit reasoning and acting phases. It:
- Builds a chat request from the context window
- Calls the LLM for reasoning and tool calls
- Executes tool actions and applies budgeting and truncation
- Enforces circuit breakers for repeated tool misuse, truncation, parse errors, and fruitless results
- Detects finish conditions and wraps up execution

```mermaid
flowchart TD
Start(["Run(ctx, taskTools, cm)"]) --> Build["Build tool definitions"]
Build --> Loop{"Within maxSteps or stepLimitFunc allows"}
Loop --> Prompt["BuildPrompt() + ChatRequest"]
Prompt --> CallLLM["LLM.Call(...)"]
CallLLM --> Resp{"Has ToolCalls?"}
Resp --> |No| EndTurn{"StopReason==end_turn?"}
EndTurn --> |Yes| Finish["Implicit finish or finish nudge"]
EndTurn --> |No| FinishAlt["Implicit finish or nudge"]
Resp --> |Yes| FirstCall["Take first tool call"]
FirstCall --> Trunc{"StopReason==max_tokens?"}
Trunc --> |Yes| AbortTrunc["Abort on consecutive truncation"]
Trunc --> |No| Circuit["Circuit breaker checks"]
Circuit --> Repeat["Repeated tool call detection"]
Repeat --> |Abort| AbortRep["Abort with message"]
Repeat --> |Nudge| InjectNudge["Inject nudge step"]
Repeat --> Exec["Execute tool"]
Exec --> Fruitless["Fruitless result detector"]
Fruitless --> |Abort| AbortFruit["Abort with message"]
Fruitless --> |Nudge| InjectFruit["Inject nudge step"]
Fruitless --> SameTool["Same-tool repetition detector"]
SameTool --> |Abort| AbortSame["Abort with message"]
SameTool --> |Nudge| InjectSame["Inject nudge step"]
SameTool --> ParseErr["Parse error tracker"]
ParseErr --> |Abort| AbortParse["Abort with message"]
ParseErr --> Budget["Apply tool result budget"]
Budget --> AddStep["AddStep to ContextManager"]
AddStep --> NextCall{"More tool calls?"}
NextCall --> |Yes| Circuit
NextCall --> |No| WrapUp["Wrap-up nudge near budget"]
WrapUp --> Fill["CheckFill() and compaction"]
Fill --> Loop
Finish --> Done(["Return ExecutorResult{Finished=true}"])
FinishAlt --> Done
AbortTrunc --> Done
AbortRep --> Done
AbortFruit --> Done
AbortSame --> Done
AbortParse --> Done
```

**Diagram sources**
- [executor.go:202-773](file://sdk/agent/executor.go#L202-L773)

Key behaviors:
- Tool result budgeting: Adaptive cap based on available tokens and hard cap, with a floor and truncation notice.
- Truncation detection: Aborts after repeated max_tokens responses with tool calls.
- Circuit breakers:
  - Repeated identical tool calls with configurable thresholds
  - Fruitless results (small non-error outputs) with configurable thresholds
  - Same-tool repetition with similar results and configurable thresholds
  - Parse errors with configurable thresholds
- Finish detection: Explicit finish tool call or implicit finish with end_turn and optional finish nudge for plan-step execution.

**Section sources**
- [executor.go:142-184](file://sdk/agent/executor.go#L142-L184)
- [executor.go:415-447](file://sdk/agent/executor.go#L415-L447)
- [executor.go:466-518](file://sdk/agent/executor.go#L466-L518)
- [executor.go:566-603](file://sdk/agent/executor.go#L566-L603)
- [executor.go:605-649](file://sdk/agent/executor.go#L605-L649)
- [executor.go:656-681](file://sdk/agent/executor.go#L656-L681)
- [executor.go:308-360](file://sdk/agent/executor.go#L308-L360)
- [executor.go:362-413](file://sdk/agent/executor.go#L362-L413)

### Subagent System: Parallel Task Execution
Subagents enable parallel execution of plan steps:
- Wraps an Executor and runs it in a goroutine
- Emits launch/completion events
- Respects context cancellation
- Aggregates results via channels

```mermaid
sequenceDiagram
participant Engine as "SDK Orchestrator"
participant Sub as "SubAgent"
participant Exec as "Executor"
participant CM as "ContextManager"
participant Tools as "ToolRegistry"
Engine->>Sub : "RunSubAgent(ctx, stepID, executor, cm, taskTools, taskDesc, emitter)"
Sub->>Sub : "Emit SubAgentLaunch"
Sub->>Exec : "Run(ctx, taskTools, cm)"
Exec->>CM : "BuildPrompt()"
Exec->>Exec : "LLM.Call + Tool calls"
Exec-->>Sub : "ExecutorResult"
Sub->>Sub : "Emit SubAgentComplete"
Sub-->>Engine : "SubAgentResult"
```

**Diagram sources**
- [subagent.go:35-96](file://sdk/agent/subagent.go#L35-L96)
- [subagent.go:98-119](file://sdk/agent/subagent.go#L98-L119)

**Section sources**
- [subagent.go:11-23](file://sdk/agent/subagent.go#L11-L23)
- [subagent.go:35-96](file://sdk/agent/subagent.go#L35-L96)
- [subagent.go:98-119](file://sdk/agent/subagent.go#L98-L119)

### Finish Detection and Completion Semantics
- FinishTool is a special tool that signals completion. The executor recognizes it and returns a finished result.
- For plan-step execution, the executor may inject a finish nudge to ensure explicit completion.
- Incomplete execution (no proper finish) is treated as a step failure.

```mermaid
flowchart TD
A["Tool call: finish"] --> B["Parse input (answer)"]
B --> C["Emit Finishing(stepNum, answer)"]
C --> D["Return ExecutorResult{Finished=true, Output=answer}"]
```

**Diagram sources**
- [finish.go:49-59](file://sdk/agent/finish.go#L49-L59)
- [executor.go:520-554](file://sdk/agent/executor.go#L520-L554)

**Section sources**
- [finish.go:12-28](file://sdk/agent/finish.go#L12-L28)
- [finish.go:49-59](file://sdk/agent/finish.go#L49-L59)
- [executor.go:326-340](file://sdk/agent/executor.go#L326-L340)
- [executor.go:520-554](file://sdk/agent/executor.go#L520-L554)

### Integration with Tool Registry, LLM Providers, and Context Management
- Tool registry: Provides tool descriptors and executes tools by name.
- LLM providers: Unified provider interface abstracts multiple LLM backends.
- Context manager: Manages token usage, compaction strategies, and prompt building.

```mermaid
classDiagram
class Executor {
+Run(ctx, taskTools, cm) ExecutorResult
+SetLogger(logger)
+SetPlanContext(stepID, index, total)
+SetStepLimitFunc(fn)
}
class ToolRegistry {
+List() []ToolDescriptor
+Execute(ctx, name, input) ToolResult
+GetToolSource(name) string
}
class LLMCaller {
+Call(ctx, req) ChatResponse
}
class ContextManager {
+BuildPrompt() []Message
+AddStep(step)
+CheckFill() FillCheck
+Compact(ctx) *CompactionResult
+AvailableTokens() int
+OutputLimit() int
}
class AgentEvents {
+StepStart(stepNum)
+Thought(stepNum, content, reasoning)
+ToolCall(stepNum, callIdx, toolName, argsPreview, source)
+ToolResult(stepNum, callIdx, resultLen, preview)
+StepComplete(stepNum, duration)
+Finishing(stepNum, summary)
+ContextFill(percent, used, max, status, stepID)
+ContextCompaction(before, after, stepID)
}
Executor --> LLMCaller : "uses"
Executor --> ToolRegistry : "uses"
Executor --> ContextManager : "uses"
Executor --> AgentEvents : "emits"
```

**Diagram sources**
- [executor.go:49-95](file://sdk/agent/executor.go#L49-L95)
- [registry.go:11-17](file://sdk/tools/registry.go#L11-L17)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)
- [context.go:106-119](file://sdk/memory/context.go#L106-L119)
- [events.go:5-26](file://sdk/agent/events.go#L5-L26)

**Section sources**
- [executor.go:101-116](file://sdk/agent/executor.go#L101-L116)
- [registry.go:91-100](file://sdk/tools/registry.go#L91-L100)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)
- [context.go:167-200](file://sdk/memory/context.go#L167-L200)
- [events.go:5-26](file://sdk/agent/events.go#L5-L26)

### Coordination with Orchestrator’s Planning and Routing
- The orchestrator routes messages, generates or resumes plans, and dispatches steps to executors.
- It configures per-step contexts, system prompts, and compaction strategies.
- It coordinates retries, replanning, and reflection to improve outcomes.

```mermaid
sequenceDiagram
participant Core as "Core Orchestrator"
participant SDK as "SDK Orchestrator"
participant BB as "Blackboard"
participant Step as "Executor/SubAgent"
Core->>Core : "injectVectorSearchHints(ctx, message)"
Core->>Core : "emitInitialContextFill()"
Core->>Core : "Route(message, tools, history)"
Core->>SDK : "Resume(BB)"
SDK->>BB : "GetPlan()"
SDK->>Step : "Create Executors for ready steps"
Step-->>SDK : "CompletedStep"
SDK-->>Core : "AggregateOutput + Reflections"
Core-->>Core : "Persist routing decision"
Core-->>User : "HandleResult"
```

**Diagram sources**
- [core_orchestrator.go:344-598](file://core/orchestrator.go#L344-L598)
- [orchestrator.go:348-752](file://sdk/orchestration/orchestrator.go#L348-L752)

**Section sources**
- [core_orchestrator.go:344-598](file://core/orchestrator.go#L344-L598)
- [orchestrator.go:348-752](file://sdk/orchestration/orchestrator.go#L348-L752)

## Dependency Analysis
- Executor depends on:
  - LLMCaller for reasoning
  - ToolExecutor for tool execution
  - ContextManager for prompt building and budgeting
  - AgentEvents for telemetry
- SubAgent depends on Executor and emits lifecycle events
- Orchestrator composes Executors into a DAG, managing retries and replanning
- ContextManager encapsulates token accounting and compaction
- ToolRegistry provides tool descriptors and execution

```mermaid
graph LR
EX["Executor"] --> LLM["LLMCaller"]
EX --> TOOLS["ToolExecutor"]
EX --> CM["ContextManager"]
EX --> EVT["AgentEvents"]
SA["SubAgent"] --> EX
ORCH["Orchestrator"] --> EX
ORCH --> CM
ORCH --> EVT
TOOLS --> REG["ToolRegistry"]
LLM --> PRV["Provider"]
```

**Diagram sources**
- [executor.go:49-95](file://sdk/agent/executor.go#L49-L95)
- [subagent.go:11-23](file://sdk/agent/subagent.go#L11-L23)
- [orchestrator.go:20-47](file://sdk/orchestration/orchestrator.go#L20-L47)
- [registry.go:11-17](file://sdk/tools/registry.go#L11-L17)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)

**Section sources**
- [executor.go:49-95](file://sdk/agent/executor.go#L49-L95)
- [subagent.go:11-23](file://sdk/agent/subagent.go#L11-L23)
- [orchestrator.go:20-47](file://sdk/orchestration/orchestrator.go#L20-L47)
- [registry.go:11-17](file://sdk/tools/registry.go#L11-L17)
- [provider.go:6-23](file://sdk/llm/provider.go#L6-L23)

## Performance Considerations
- Token budgeting: The executor estimates observation sizes and truncates results to fit within the available token budget, with a minimum floor to avoid useless truncation.
- Adaptive caps: The effective cap is the minimum of a hard cap and a fraction of available tokens, preventing context overflow.
- Compaction: When context fill reaches thresholds, the executor triggers compaction to reduce token usage and prevent rejection.
- Parallelism: Subagents enable parallel execution of independent steps, improving throughput.
- Streaming: The orchestrator supports streaming assistant chunks and aggregates them for UI rendering.

[No sources needed since this section provides general guidance]

## Troubleshooting Guide
Common issues and mitigations:
- Repeated tool misuse: Circuit breaker triggers nudges or aborts after repeated identical tool calls. Adjust thresholds or restructure the plan to vary tool arguments.
- Truncation: If responses are truncated due to output limits, the executor aborts after a configurable threshold. Reduce output size or split operations.
- Fruitless results: Consecutive minimal results trigger nudges or aborts. Vary search parameters or switch to different tools.
- Same-tool repetition: Similar results with varied arguments suggest a fundamental limitation. Summarize findings and call finish.
- Parse errors: Repeated parse failures on the same tool trigger aborts. Verify tool input schemas and reduce argument size.
- Context overflow: When fill status is reject, compaction is attempted; if still failing, increase model context or reduce step complexity.
- Step limit: When the step limit is reached, the executor invokes a user callback to decide allowance. Use “allow_once” or “allow_always” judiciously.

**Section sources**
- [executor.go:466-518](file://sdk/agent/executor.go#L466-L518)
- [executor.go:415-447](file://sdk/agent/executor.go#L415-L447)
- [executor.go:566-603](file://sdk/agent/executor.go#L566-L603)
- [executor.go:605-649](file://sdk/agent/executor.go#L605-L649)
- [executor.go:656-681](file://sdk/agent/executor.go#L656-L681)
- [context.go:106-128](file://sdk/memory/context.go#L106-L128)
- [context.go:400-437](file://sdk/memory/context.go#L400-L437)

## Conclusion
The C0WRK agent executor system implements a robust ReAct loop with strong safeguards against misuse, truncation, and fruitless loops. It integrates tightly with the orchestrator to coordinate planning, routing, and parallel execution, while providing comprehensive eventing and budgeting. The finish detection mechanism ensures explicit completion, and the subagent system enables efficient parallelism. Together, these components deliver a reliable, observable, and extensible execution engine.

[No sources needed since this section summarizes without analyzing specific files]

## Appendices

### Example Workflows
- ReAct loop with tool calls:
  - Build prompt from context
  - LLM returns reasoning and tool calls
  - Execute tools, apply budgeting, and add observations
  - Repeat until finish or budget exhaustion
- Circuit breaker behavior:
  - Repeated identical tool calls: Nudge, then abort after threshold
  - Truncation: Abort after consecutive max_tokens with tool calls
  - Fruitless results: Nudge, then abort after threshold
  - Same-tool repetition: Nudge, then abort after threshold
  - Parse errors: Abort after repeated failures
- Subagent coordination:
  - Multiple ready steps dispatched in parallel
  - Results aggregated and reflected upon completion
- Integration with orchestration:
  - Plan generation and execution
  - Reflection and replanning
  - Persistence and routing decisions

[No sources needed since this section provides general guidance]