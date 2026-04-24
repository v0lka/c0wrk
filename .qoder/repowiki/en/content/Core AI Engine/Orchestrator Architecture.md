# Orchestrator Architecture

<cite>
**Referenced Files in This Document**
- [orchestrator.go](file://core/orchestrator.go)
- [router.go](file://core/router.go)
- [planner.go](file://core/planner.go)
- [types.go](file://core/types.go)
- [builder.go](file://core/builder.go)
- [builderconfig.go](file://core/builderconfig.go)
- [reflector.go](file://core/reflector.go)
- [emitter_logging.go](file://core/emitter_logging.go)
- [systemprompt.go](file://core/systemprompt.go)
- [application.go](file://backend/application.go)
- [config.go](file://sdk/orchestration/config.go)
- [interfaces.go](file://sdk/orchestration/interfaces.go)
- [orchestrator.go](file://sdk/orchestration/orchestrator.go)
- [blackboard.go](file://sdk/orchestration/blackboard.go)
- [doc.go](file://sdk/orchestration/doc.go)
- [main.go](file://main.go)
- [AGENTS.md](file://AGENTS.md)
</cite>

## Update Summary
**Changes Made**
- Added documentation for the new AGENTS.md integration feature
- Updated the Orchestrator component analysis to include vector search hint injection
- Enhanced the system prompt building process to incorporate AGENTS.md content
- Added new sections covering the automatic detection and processing of AGENTS.md files
- Updated architecture diagrams to reflect the new vector search hint injection mechanism

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

## Introduction
This document explains the C0WRK orchestrator architecture as the central coordinator of the ReAct loop workflow. It integrates planning, reasoning, and execution phases while managing conversation history, routing decisions, and event emission. The orchestrator delegates the Plan&Execute loop to the SDK orchestration engine, while retaining control over routing, context management, persistence, and the new vector search hint injection mechanism that automatically detects and processes AGENTS.md files in workspace root.

## Project Structure
The orchestrator spans the core orchestration layer and the SDK orchestration engine:
- Core orchestrator and supporting components live under core/.
- The SDK orchestration engine resides under sdk/orchestration/.
- Backend integration and factory pattern are implemented in backend/ and exposed via Application.
- AGENTS.md integration provides automatic project instruction injection from workspace root.

```mermaid
graph TB
subgraph "Core Layer"
CORE_ORCH["core/orchestrator.go"]
CORE_ROUTER["core/router.go"]
CORE_PLANNER["core/planner.go"]
CORE_TYPES["core/types.go"]
CORE_REFLECTOR["core/reflector.go"]
CORE_BUILDER["core/builder.go"]
CORE_EMIT_LOG["core/emitter_logging.go"]
CORE_SYSPROMPT["core/systemprompt.go"]
CORE_AGENTS["AGENTS.md"]
end
subgraph "SDK Engine"
SDK_CFG["sdk/orchestration/config.go"]
SDK_IFACES["sdk/orchestration/interfaces.go"]
SDK_ORCH["sdk/orchestration/orchestrator.go"]
SDK_BB["sdk/orchestration/blackboard.go"]
SDK_DOC["sdk/orchestration/doc.go"]
end
subgraph "Backend Integration"
BACK_APP["backend/application.go"]
MAIN_GO["main.go"]
end
CORE_ORCH --> SDK_ORCH
CORE_ORCH --> CORE_ROUTER
CORE_ORCH --> CORE_PLANNER
CORE_ORCH --> CORE_REFLECTOR
CORE_ORCH --> CORE_TYPES
CORE_ORCH --> CORE_BUILDER
CORE_ORCH --> CORE_EMIT_LOG
CORE_ORCH --> CORE_SYSPROMPT
CORE_ORCH --> CORE_AGENTS
SDK_ORCH --> SDK_CFG
SDK_ORCH --> SDK_IFACES
SDK_ORCH --> SDK_BB
BACK_APP --> CORE_BUILDER
BACK_APP --> CORE_ORCH
MAIN_GO --> BACK_APP
```

**Diagram sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [router.go:22-114](file://core/router.go#L22-L114)
- [planner.go:168-276](file://core/planner.go#L168-L276)
- [types.go:107-167](file://core/types.go#L107-L167)
- [reflector.go:20-80](file://core/reflector.go#L20-L80)
- [builder.go:27-108](file://core/builder.go#L27-L108)
- [emitter_logging.go:10-31](file://core/emitter_logging.go#L10-L31)
- [systemprompt.go:14-66](file://core/systemprompt.go#L14-L66)
- [config.go:11-62](file://sdk/orchestration/config.go#L11-L62)
- [interfaces.go:12-59](file://sdk/orchestration/interfaces.go#L12-L59)
- [orchestrator.go:17-47](file://sdk/orchestration/orchestrator.go#L17-L47)
- [blackboard.go:16-60](file://sdk/orchestration/blackboard.go#L16-L60)
- [doc.go:1-4](file://sdk/orchestration/doc.go#L1-L4)
- [application.go:41-133](file://backend/application.go#L41-L133)
- [main.go:18-44](file://main.go#L18-L44)

**Section sources**
- [orchestrator.go:1-599](file://core/orchestrator.go#L1-L599)
- [config.go:1-81](file://sdk/orchestration/config.go#L1-L81)
- [interfaces.go:1-88](file://sdk/orchestration/interfaces.go#L1-L88)
- [application.go:1-270](file://backend/application.go#L1-L270)

## Core Components
- Orchestrator: Central coordinator that routes, decides between synthetic/full planning, and delegates Plan&Execute to the SDK engine. Manages conversation history, routing decision persistence, event emission, and vector search hint injection including AGENTS.md processing.
- Router: Determines domain and complexity of user requests and whether clarification is needed.
- Planner: Generates DAG execution plans, supports replan and continuation planning, and optionally uses exploration for informed planning. Incorporates AGENTS.md project instructions into system prompts.
- Reflector: Produces structured self-correction insights after step failures.
- Emitter: Emits orchestration-level events (routing, plan generation, retries, reflections).
- OrchestratorBuilder: Factory that builds per-session Orchestrators with shared tool registry, MCP gateway, and LLM router.
- SDK Orchestrator: Generic Plan&Execute engine that executes DAG plans, manages retries, replanning, and reflection.
- Blackboard: Shared state container for plan, step results, reflections, and file changes.
- Vector Search Hints: Automatic file discovery mechanism that enhances context with relevant project files and AGENTS.md content.

**Section sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [router.go:22-114](file://core/router.go#L22-L114)
- [planner.go:168-276](file://core/planner.go#L168-L276)
- [reflector.go:20-80](file://core/reflector.go#L20-L80)
- [types.go:107-167](file://core/types.go#L107-L167)
- [builder.go:27-108](file://core/builder.go#L27-L108)
- [config.go:11-62](file://sdk/orchestration/config.go#L11-L62)
- [blackboard.go:16-60](file://sdk/orchestration/blackboard.go#L16-L60)

## Architecture Overview
The orchestrator sits between the backend session manager and the SDK orchestration engine. It:
- Routes incoming messages to determine domain and complexity.
- Chooses synthetic or full planning based on complexity threshold.
- Injects vector search hints including AGENTS.md content for enhanced context.
- Builds a plan and delegates execution to the SDK engine.
- Persists routing decisions and maintains conversation history.
- Emits orchestration events for UI and persistence.

```mermaid
sequenceDiagram
participant Client as "Client"
participant App as "Application"
participant Builder as "OrchestratorBuilder"
participant Orchestrator as "Core Orchestrator"
participant Router as "Router"
participant Planner as "Planner"
participant VectorSearch as "Vector Search"
participant AGENTS as "AGENTS.md"
participant SDK as "SDK Orchestrator"
participant Emitter as "Emitter"
Client->>App : "User message"
App->>Builder : "Build Orchestrator"
Builder-->>App : "Orchestrator instance"
App->>Orchestrator : "HandleMessage(message)"
Orchestrator->>Emitter : "ServiceWithMeta('Routing request...')"
Orchestrator->>Router : "Route(message, tools, history)"
Router-->>Orchestrator : "RoutingDecision"
Orchestrator->>Emitter : "Routing(mode, domain, complexity)"
alt Complexity <= Threshold
Orchestrator->>Planner : "CreateSyntheticPlan(message, domain)"
else Complexity > Threshold
Orchestrator->>Emitter : "ServiceWithMeta('Planning approach...')"
Orchestrator->>VectorSearch : "Query vector index (2s timeout)"
VectorSearch-->>Orchestrator : "Vector search results"
Orchestrator->>AGENTS : "Read workspace/AGENTS.md"
AGENTS-->>Orchestrator : "Project instructions"
Orchestrator->>Planner : "Plan(message, tools, reflections)"
end
Planner-->>Orchestrator : "Plan with AGENTS.md context"
Orchestrator->>SDK : "Resume(blackboard with plan)"
SDK-->>Orchestrator : "ExecutionResult"
Orchestrator->>Emitter : "PlanGenerated/Step events"
Orchestrator->>Orchestrator : "Persist routing decision"
Orchestrator-->>App : "HandleResult"
App-->>Client : "Response"
```

**Diagram sources**
- [application.go:104-133](file://backend/application.go#L104-L133)
- [builder.go:108-208](file://core/builder.go#L108-L208)
- [orchestrator.go:338-598](file://core/orchestrator.go#L338-L598)
- [router.go:45-114](file://core/router.go#L45-L114)
- [planner.go:257-276](file://core/planner.go#L257-L276)
- [orchestrator.go:85-126](file://sdk/orchestration/orchestrator.go#L85-L126)

## Detailed Component Analysis

### Orchestrator
The Orchestrator is the central coordinator that:
- Integrates Router, Planner, LLM caller, ToolRegistry, and ContextManagerFactory.
- Manages conversation history and routing decision persistence.
- Emits orchestration events via an Emitter.
- Uses the SDK Orchestrator for Plan&Execute execution.
- Supports synthetic planning for low-complexity tasks and continuation planning for follow-ups.
- **NEW**: Implements vector search hint injection with automatic AGENTS.md processing.

Key responsibilities:
- Handle and HandleMessage entry points.
- Routing and clarification handling.
- Conversation history management with configurable retention.
- Persistence of routing decisions and task completion.
- Wiring TrackingCaller for per-step context tracking.
- **NEW**: Vector search hint injection with AGENTS.md content processing.
- **NEW**: Context-aware system prompt building with project instructions.

```mermaid
classDiagram
class Orchestrator {
-engine : orchestration.Orchestrator
-router : Router
-planner : Planner
-llm : LLMCaller
-toolRegistry : ToolRegistry
-config : OrchestratorConfig
-contextFactory : ContextManagerFactory
-logger : slog.Logger
-emitter : Emitter
-modelRegistry : ModelRegistry
-bbFactory : BlackboardFactory
-conversationHistory : []Message
-taskStore : TaskPersistence
-bbRestoreFunc : BlackboardRestoreFunc
-trackingCaller : TrackingCaller
-vectorSearchFunc : VectorSearchFunc
+Handle(ctx, message) HandleResult
+HandleMessage(ctx, message, sessionID, opts) HandleResult
+Resume(ctx, bb, routing) HandleResult
+SetTaskStore(store)
+SetBlackboardRestoreFunc(fn)
+injectVectorSearchHints(ctx, query) context.Context
}
class Router {
+Route(ctx, message, tools, history) RoutingDecision
}
class Planner {
+Plan(ctx, task, tools, reflections) Plan
+PlanContinuation(ctx, originalRequest, existingPlan, completedSteps, newMessage, availableTools) Plan
+Replan(ctx, originalPlan, completedSteps, failedStep, reflection, reflections) Plan
+formatAgentsMD(ctx) string
}
class Emitter {
+Routing(mode, domain, complexity)
+PlanGenerated(count, steps)
+PlanStepStart(id, desc, summary)
+PlanStepComplete(id, success, duration, errMsg)
+Reflection(reflection, attempt, max)
+Retry(attempt, max)
+StepRetry(stepID, attempt, max)
+Service(content)
+ServiceWithMeta(content, meta)
+ReplanFailed(err)
+FileRollbackError(stepID, err)
}
class SystemPrompt {
+buildSystemPrompt(ctx, userMessage, modelMeta) string
+formatVectorSearchHints(ctx) string
+formatAgentsMD(ctx) string
}
Orchestrator --> Router : "uses"
Orchestrator --> Planner : "uses"
Orchestrator --> Emitter : "emits events"
Orchestrator --> SystemPrompt : "builds prompts"
```

**Diagram sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [router.go:22-114](file://core/router.go#L22-L114)
- [planner.go:168-276](file://core/planner.go#L168-L276)
- [types.go:107-167](file://core/types.go#L107-L167)
- [systemprompt.go:68-124](file://core/systemprompt.go#L68-L124)

**Section sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [orchestrator.go:338-598](file://core/orchestrator.go#L338-L598)

### Vector Search Hint Injection and AGENTS.md Processing
**NEW**: The orchestrator now implements automatic vector search hint injection with AGENTS.md integration:

- **Vector Search Integration**: Queries the vector index for relevant files with a 2-second timeout.
- **AGENTS.md Detection**: Automatically scans the workspace root for AGENTS.md files.
- **Context Enhancement**: Injects both vector search results and AGENTS.md content into the execution context.
- **Priority Handling**: Places AGENTS.md as the first hint to ensure project instructions take precedence.
- **Graceful Degradation**: Works even when vector search fails or AGENTS.md is missing.

```mermaid
flowchart TD
Start(["injectVectorSearchHints Entry"]) --> CheckVector{"vectorSearchFunc != nil?"}
CheckVector --> |Yes| VectorSearch["Query vector index (2s timeout)"]
CheckVector --> |No| CheckAgents["Check workspace for AGENTS.md"]
VectorSearch --> ProcessResults{"Results found?"}
ProcessResults --> |Yes| BuildHints["Build VectorSearchHints"]
ProcessResults --> |No| CheckAgents
BuildHints --> CheckAgents
CheckAgents --> CheckWS{"Workspace path exists?"}
CheckWS --> |Yes| ReadAgents["Read AGENTS.md from workspace root"]
CheckWS --> |No| ReturnCtx["Return context"]
ReadAgents --> AgentsFound{"File found?"}
AgentsFound --> |Yes| InjectAgents["Inject AGENTS.md via WithAgentsMD"]
InjectAgents --> PrependHint["Prepend AGENTS.md as first hint"]
AgentsFound --> |No| ReturnCtx
PrependHint --> CheckHints{"hints != nil && len(hints.Files) > 0?"}
CheckHints --> |Yes| InjectHints["Inject VectorSearchHints"]
CheckHints --> |No| ReturnCtx
InjectHints --> ReturnCtx
ReturnCtx --> End(["Return enhanced context"])
```

**Diagram sources**
- [orchestrator.go:279-337](file://core/orchestrator.go#L279-L337)

**Section sources**
- [orchestrator.go:273-337](file://core/orchestrator.go#L273-L337)

### Router
The Router classifies user requests by domain and complexity, and determines whether clarification is needed. It:
- Builds grouped tool lists for prompts.
- Constructs system and user messages.
- Calls the LLM to produce a JSON-formatted RoutingDecision.
- Validates and clamps domain and complexity values.

```mermaid
flowchart TD
Start(["Route Entry"]) --> BuildTools["Build grouped tool list"]
BuildTools --> BuildPrompt["Build system prompt with instructions"]
BuildPrompt --> BuildMessages["Assemble messages (history + user)"]
BuildMessages --> CallLLM["Call LLM"]
CallLLM --> Parse["Extract JSON and parse RoutingDecision"]
Parse --> Validate["Validate domain and clamp complexity"]
Validate --> Return(["Return RoutingDecision"])
```

**Diagram sources**
- [router.go:45-114](file://core/router.go#L45-L114)

**Section sources**
- [router.go:22-172](file://core/router.go#L22-L172)

### Planner
The Planner generates DAG execution plans:
- Direct planning for general domain or when no exploration tools are available.
- Informed exploration planning for specialized domains, using a bounded ReAct loop to discover a plan.
- Replan after failures and PlanContinuation for follow-ups.
- Supports step-specific tool filtering and domain assignment.
- **NEW**: Incorporates AGENTS.md project instructions into system prompts with strict adherence requirements.

```mermaid
flowchart TD
Start(["Plan Entry"]) --> CheckDomain{"Domain == 'general' or no planner tools?"}
CheckDomain --> |Yes| DirectPlan["planDirect()"]
CheckDomain --> |No| ExplorePlan["planWithExploration()"]
ExplorePlan --> Exec["Run internal Executor with planner tools"]
Exec --> ParsePlan["Parse plan from executor output"]
DirectPlan --> AppendContext["Append system prompt context"]
ParsePlan --> AppendContext
AppendContext --> FormatAgents["formatAgentsMD(ctx)"]
FormatAgents --> Return(["Return Plan"])
```

**Diagram sources**
- [planner.go:257-421](file://core/planner.go#L257-L421)

**Section sources**
- [planner.go:168-573](file://core/planner.go#L168-L573)

### System Prompt Building with AGENTS.md Integration
**NEW**: Enhanced system prompt building process incorporates AGENTS.md content:

- **Context Priority**: AGENTS.md content takes precedence over vector search hints.
- **Strict Adherence**: System prompts instruct planners to strictly follow AGENTS.md instructions.
- **Contradiction Handling**: Provides guidance for resolving contradictions between project instructions and codebase.
- **Automatic Injection**: Seamlessly integrates AGENTS.md content into both planner and executor prompts.

```mermaid
flowchart TD
Start(["buildSystemPrompt Entry"]) --> BuildBase["Build base system prompt"]
BuildBase --> CheckPlanMode{"PlanModeKey set?"}
CheckPlanMode --> |Yes| AddPlanContext["Add OrchestratorPlanContext"]
CheckPlanMode --> |No| AddReactContext["Add ReAct completion mandate"]
AddPlanContext --> CheckEnv{"Environment context available?"}
AddReactContext --> CheckEnv
CheckEnv --> |Yes| AddEnv["Append environment block"]
CheckEnv --> |No| CheckHints{"VectorSearchHints available?"}
AddEnv --> CheckHints
CheckHints --> |Yes| AddHints["Append vector search hints"]
CheckHints --> |No| CheckAgents{"AgentsMD available?"}
AddHints --> CheckAgents
CheckAgents --> |Yes| AddAgents["Append AGENTS.md content"]
CheckAgents --> |No| Return(["Return final prompt"])
AddAgents --> Return
```

**Diagram sources**
- [systemprompt.go:68-124](file://core/systemprompt.go#L68-L124)

**Section sources**
- [systemprompt.go:68-124](file://core/systemprompt.go#L68-L124)

### Reflector
The Reflector produces structured self-correction insights:
- Builds a system prompt and user message containing trajectory, plan, and previous reflections.
- Calls the LLM to produce a Reflection with suggested actions.
- Validates suggested actions and ensures defaults.

```mermaid
sequenceDiagram
participant Orchestrator as "Core Orchestrator"
participant Reflector as "Reflector"
participant LLM as "LLM"
Orchestrator->>Reflector : "Reflect(trajectory, plan, prevReflections)"
Reflector->>Reflector : "Build system prompt + user message"
Reflector->>LLM : "ChatRequest"
LLM-->>Reflector : "Reflection JSON"
Reflector->>Reflector : "Validate suggested action"
Reflector-->>Orchestrator : "Reflection"
```

**Diagram sources**
- [reflector.go:39-80](file://core/reflector.go#L39-L80)

**Section sources**
- [reflector.go:20-177](file://core/reflector.go#L20-L177)

### Emitter and Event Emission
The Emitter interface extends SDK agent events with orchestration-specific signals:
- Routing, PlanGenerated, PlanStepStart/Complete, Reflection, Retry, StepRetry, Service/ServiceWithMeta, ReplanFailed, FileRollbackError.
- A logging wrapper emits structured logs for all events.
- An adapter bridges core Emitter to SDK Events.

```mermaid
classDiagram
class Emitter {
<<interface>>
+Routing(mode, domain, complexity)
+PlanGenerated(stepCount, steps)
+PlanStepStart(stepID, description, summary)
+PlanStepComplete(stepID, success, duration, errMsg)
+Reflection(reflection, attempt, max)
+Retry(attempt, max)
+StepRetry(stepID, attempt, max)
+Service(content)
+ServiceWithMeta(content, meta)
+ReplanFailed(err)
+FileRollbackError(stepID, err)
}
class loggingEmitter {
-inner : Emitter
-logger : slog.Logger
+WithPlanStepID(id) Emitter
+WithRetryAttempt(attempt) Emitter
+Routing(...)
+PlanGenerated(...)
+...
}
class emitterEventsAdapter {
-Emitter : Emitter
-logger : slog.Logger
+OnPlanGenerated(...)
+OnStepStarted(...)
+...
}
loggingEmitter ..|> Emitter
emitterEventsAdapter ..|> Events
```

**Diagram sources**
- [types.go:107-167](file://core/types.go#L107-L167)
- [emitter_logging.go:10-199](file://core/emitter_logging.go#L10-L199)

**Section sources**
- [types.go:107-234](file://core/types.go#L107-L234)
- [emitter_logging.go:10-199](file://core/emitter_logging.go#L10-L199)

### OrchestratorBuilder and Factory Pattern
The OrchestratorBuilder encapsulates shared infrastructure and builds per-session Orchestrators:
- Shared tool registry and MCP gateway.
- Fresh LLM router and model registry per session.
- Context factory for compaction strategies.
- Logging and dumping wrappers for LLM callers.
- Factory closure passed to the session manager.

```mermaid
sequenceDiagram
participant Backend as "Backend Application"
participant Builder as "OrchestratorBuilder"
participant Router as "LLM Router"
participant Tracking as "TrackingCaller"
participant ContextFactory as "ContextManagerFactory"
participant Orchestrator as "Core Orchestrator"
Backend->>Builder : "Build(builderCfg, emitter, logger, bbFactory, stepLimitFunc, dumpWriter)"
Builder->>Router : "buildRouter(builderCfg)"
Builder->>Tracking : "NewTrackingCaller(router)"
Builder->>ContextFactory : "buildContextFactory(trackingCaller, cfg, dumpWriter)"
Builder-->>Backend : "factory closure"
Backend->>Orchestrator : "factory(emitter, logger, workspacePath, bbFactory, dumpWriter)"
Orchestrator-->>Backend : "Orchestrator instance"
```

**Diagram sources**
- [builder.go:108-208](file://core/builder.go#L108-L208)
- [builder.go:381-541](file://core/builder.go#L381-L541)

**Section sources**
- [builder.go:27-108](file://core/builder.go#L27-L108)
- [builder.go:108-208](file://core/builder.go#L108-L208)
- [builder.go:381-541](file://core/builder.go#L381-L541)

### SDK Orchestrator and Plan&Execute Loop
The SDK Orchestrator runs the DAG-based execution loop:
- Creates or resumes with a blackboard.
- Executes ready steps in parallel, tracking per-step results and file changes.
- Handles per-step retries, reflection, replan, and outer-level retries.
- Aggregates outputs and manages final results.

```mermaid
flowchart TD
Start(["Execute/Resume"]) --> InitBB["Initialize/Load Blackboard"]
InitBB --> PlanCheck{"Has plan?"}
PlanCheck --> |No| GeneratePlan["Planner.Plan(...)"]
PlanCheck --> |Yes| LoadPlan["Load existing plan"]
GeneratePlan --> SetPlan["Set plan on blackboard"]
SetPlan --> RunLoop["runPlanExecute(...)"]
LoadPlan --> RunLoop
RunLoop --> Ready["Find ready steps"]
Ready --> |None| Success["Aggregate output and return"]
Ready --> Dispatch["Dispatch parallel executors"]
Dispatch --> PerStepRetry{"Any failed?"}
PerStepRetry --> |Yes| Reflect["Reflector.Reflect(...)"]
Reflect --> Decide{"SuggestedAction?"}
Decide --> |Replan| Replan["Planner.Replan(...)"]
Decide --> |Retry| Retry["Retry failed steps"]
Decide --> |Abort| Abort["Abort and return"]
Replan --> RunLoop
Retry --> RunLoop
Abort --> Success
```

**Diagram sources**
- [orchestrator.go:56-126](file://sdk/orchestration/orchestrator.go#L56-L126)
- [orchestrator.go:128-346](file://sdk/orchestration/orchestrator.go#L128-L346)
- [orchestrator.go:348-752](file://sdk/orchestration/orchestrator.go#L348-L752)

**Section sources**
- [orchestrator.go:17-47](file://sdk/orchestration/orchestrator.go#L17-L47)
- [orchestrator.go:56-126](file://sdk/orchestration/orchestrator.go#L56-L126)
- [orchestrator.go:128-346](file://sdk/orchestration/orchestrator.go#L128-L346)
- [orchestrator.go:348-752](file://sdk/orchestration/orchestrator.go#L348-L752)

### Blackboard and State Management
The Blackboard provides shared state:
- Stores original request, plan, step results, reflections, and final result.
- Tracks file changes per step and aggregates session-wide changes.
- Supports fact memory for inter-step communication and search.

```mermaid
classDiagram
class Blackboard {
<<interface>>
+GetOriginalRequest() string
+GetPlan() *Plan
+GetStepResult(stepID) (StepResult, bool)
+GetStepSummary(stepID) string
+GetAllStepResults() map[string]StepResult
+GetReflections() []Reflection
+GetFinalResult() string
+SetOriginalRequest(req)
+SetPlan(plan)
+SetStepResult(stepID, output, err, steps)
+AddReflection(r)
+SetFinalResult(result)
+Search(query) []BlackboardEntry
+SetStepFileChanges(stepID, changes)
+GetStepFileChanges(stepID) []FileChange
+GetAllFileChanges() map[string][]FileChange
+GetSessionFileChanges() []FileChange
+StoreFact(fact)
+SearchFacts(keywords) []Fact
}
class MapBlackboard {
-mu : RWMutex
-request : string
-plan : *Plan
-stepResults : map[string]StepResult
-reflections : []Reflection
-finalResult : string
-fileChanges : map[string][]FileChange
-facts : []Fact
+Get/Set methods
+Search
+StoreFact/SearchFacts
+SetStepFileChanges/GetSessionFileChanges
}
MapBlackboard ..|> Blackboard
```

**Diagram sources**
- [interfaces.go:61-87](file://sdk/orchestration/interfaces.go#L61-L87)
- [blackboard.go:16-60](file://sdk/orchestration/blackboard.go#L16-L60)
- [blackboard.go:62-564](file://sdk/orchestration/blackboard.go#L62-L564)

**Section sources**
- [interfaces.go:61-87](file://sdk/orchestration/interfaces.go#L61-L87)
- [blackboard.go:16-564](file://sdk/orchestration/blackboard.go#L16-L564)

## Dependency Analysis
The orchestrator's dependencies are intentionally decoupled:
- Core Orchestrator depends on Router, Planner, LLM caller, ToolRegistry, ContextManagerFactory, Emitter, ModelRegistry, and optional persistence hooks.
- The SDK Orchestrator depends on Planner, LLM, Tools, ToolRegistry, TokenCounter, ModelRegistry, ContextFactory, Events, and step configuration.
- Backend Application composes OrchestratorBuilder and passes a factory closure to the session manager.
- **NEW**: Vector search integration provides optional context enhancement without breaking changes.

```mermaid
graph TB
CORE_ORCH["Core Orchestrator"] --> SDK_ORCH["SDK Orchestrator"]
CORE_ORCH --> ROUTER["Router"]
CORE_ORCH --> PLANNER["Planner"]
CORE_ORCH --> EMITTER["Emitter"]
CORE_ORCH --> TYPES["Types (ContextManager, Emitter, Blackboard)"]
CORE_ORCH --> BUILDER["OrchestratorBuilder"]
CORE_ORCH --> SDK_IFACES["SDK Interfaces"]
CORE_ORCH --> SDK_CFG["SDK Config"]
CORE_ORCH --> VECTOR_SEARCH["Vector Search"]
CORE_ORCH --> AGENTS_MD["AGENTS.md"]
BACK_APP["Backend Application"] --> BUILDER
BACK_APP --> CORE_ORCH
```

**Diagram sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [config.go:11-62](file://sdk/orchestration/config.go#L11-L62)
- [interfaces.go:12-59](file://sdk/orchestration/interfaces.go#L12-L59)
- [application.go:104-133](file://backend/application.go#L104-L133)

**Section sources**
- [orchestrator.go:55-189](file://core/orchestrator.go#L55-L189)
- [config.go:11-62](file://sdk/orchestration/config.go#L11-L62)
- [interfaces.go:12-59](file://sdk/orchestration/interfaces.go#L12-L59)
- [application.go:104-133](file://backend/application.go#L104-L133)

## Performance Considerations
- Synthetic planning reduces token usage for low-complexity tasks by bypassing the Planner LLM call when complexity is below the threshold.
- Sliding window compaction keeps recent context relevant for long executions; hierarchical compaction is used for complex general tasks.
- Tool result budgets and circuit breakers prevent runaway outputs and repeated failures.
- Parallel step execution maximizes throughput while respecting per-step budgets and retries.
- **NEW**: Vector search hints are processed with 2-second timeouts to prevent blocking operations.
- **NEW**: AGENTS.md processing is lightweight and only occurs when workspace path is available.
- **NEW**: Graceful degradation ensures system continues functioning even when vector search or AGENTS.md are unavailable.

## Troubleshooting Guide
Common issues and diagnostics:
- Routing failures: Verify Router LLM availability and prompt formatting; check JSON extraction and validation.
- Planning failures: Inspect Planner exploration executor outcomes and fallback to direct planning; review tool availability and model metadata.
- Reflection failures: Ensure Reflection JSON parsing and suggested action validation; confirm environment context injection.
- Execution errors: Review per-step retry loops, replan outcomes, and file rollback errors; check circuit breaker thresholds.
- Event emission: Use logging emitter to trace orchestration events and step-level diagnostics.
- **NEW**: Vector search hint injection failures: Check vector search function configuration and workspace permissions.
- **NEW**: AGENTS.md processing issues: Verify workspace path existence and file permissions for AGENTS.md.
- **NEW**: Context priority problems: Ensure AGENTS.md is properly formatted and contains valid project instructions.

**Section sources**
- [router.go:85-114](file://core/router.go#L85-L114)
- [planner.go:390-421](file://core/planner.go#L390-L421)
- [reflector.go:150-177](file://core/reflector.go#L150-L177)
- [emitter_logging.go:10-199](file://core/emitter_logging.go#L10-L199)

## Conclusion
The C0WRK orchestrator provides a robust, extensible framework for ReAct-based task orchestration. By delegating the Plan&Execute loop to the SDK engine while maintaining control over routing, planning strategy, context management, and persistence, it achieves a balance between flexibility and reliability. The factory pattern and backend integration enable dynamic configuration and seamless UI integration. 

**NEW**: The integration of AGENTS.md processing and vector search hint injection significantly enhances the system's ability to provide contextually relevant project instructions and file references, improving task execution quality while maintaining graceful degradation and performance optimization. This enhancement demonstrates the orchestrator's commitment to evolving with project needs while preserving backward compatibility and system stability.