package core

// SkillsRelativePath is the conventional relative path from a project workspace
// root to the project-local agent skills directory. Shared between core/ and
// backend/config/ to avoid duplicating the ".agents/skills" literal.
const SkillsRelativePath = ".agents/skills"

// AgentsRelativePath is the conventional relative path from a project workspace
// root to the project-local Subagent Profiles directory (AGENT.md files).
// Shared between core/ and backend/config/ to avoid duplicating the
// ".agents/agents" literal. Mirrors SkillsRelativePath for the agents package.
const AgentsRelativePath = ".agents/agents"

// EmbeddedRuntimesRelativePath is the relative path, within the c0wrk default
// agent directory, of the embedded-LLM inference-runtime root
// (~/.c0wrk/runtimes). Each pinned runtime is extracted into its own
// "llama-<tag>-<backend>" subdirectory, which core/embeddedllm derives.
//
// The directory is deliberately NOT under tools/: Manager.PrependToPATH()
// exposes <toolsDir>/bin to the agent's bash_exec, and the inference runtime
// must never be agent-invokable (ADR-066 D3, ASI05).
//
// The constant lives here — paralleling SkillsRelativePath — so the layer that
// owns c0wrk's directory layout (backend/config/paths.go RuntimesDir) and the
// subsystem that owns the embedded layout agree on one literal.
// core/embeddedllm itself cannot import this package (root core imports
// core/embeddedllm, so an import back would cycle); it receives the resolved
// root from its caller instead of re-deriving it.
const EmbeddedRuntimesRelativePath = "runtimes"

// EmbeddedModelRelativePath is the relative path, within the c0wrk default
// agent directory, of the embedded-LLM weights directory
// (~/.c0wrk/models/bonsai-2-27b). It is a dedicated subdirectory of the
// models/ root — that root already holds the flat embedding-model files
// resolved by desktop/startup.go resolveModelPath, so nesting keeps Remove
// from ever touching the embedding model (ADR-066, Alternatives Considered).
const EmbeddedModelRelativePath = "models/bonsai-2-27b"

// WorktreesRelativePath is the conventional relative path from a project
// repository root to the app-managed session-worktree container
// (<repo>/.worktrees/<name> per ADR-080). Shared between core/ (worktree
// primitive classification in core/workspace) and backend/config (the
// ManagedWorktreesDir/ManagedWorktreePath constructors) so the two layers
// agree on one literal — mirroring SkillsRelativePath.
const WorktreesRelativePath = ".worktrees"

// GitSafeHooksRelativePath is the relative path, within the c0wrk default
// agent directory, of the empty directory every git invocation points at via
// "-c core.hooksPath=<dir>" (see internal/sysproc.GitCmd). Hooks from the
// repository under inspection are thereby never executed. The constant lives
// here — paralleling SkillsRelativePath — so cross-layer code and tests can
// reference the canonical value. internal/sysproc keeps a test-pinned
// duplicate of this literal because it cannot import this package (core
// imports core/markitdown, which imports internal/sysproc — an import here
// would cycle).
const GitSafeHooksRelativePath = "git/safe-hooks"
