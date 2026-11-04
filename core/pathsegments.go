package core

// SkillsRelativePath is the project-relative path to the project-local agent
// skills directory, shared across layers. See specs/decisions/021-subagents.md.
const SkillsRelativePath = ".agents/skills"

// AgentsRelativePath is the project-relative Subagent Profiles directory
// (AGENT.md files), mirroring SkillsRelativePath. See specs/decisions/021-subagents.md.
const AgentsRelativePath = ".agents/agents"

// EmbeddedRuntimesRelativePath is the embedded-LLM inference-runtime root in
// the c0wrk default agent directory (~/.c0wrk/runtimes). Deliberately NOT
// under tools/: <toolsDir>/bin is exposed to the agent's bash_exec, and the
// inference runtime must never be agent-invokable (ADR-066 D3, ASI05);
// core/embeddedllm receives the resolved root from its caller (import cycle).
// See specs/domains/embedded-llm.md.
const EmbeddedRuntimesRelativePath = "runtimes"

// EmbeddedModelRelativePath is the embedded-LLM weights directory — a dedicated
// subdir of the models/ root (which holds the flat embedding-model files), so
// Remove can never touch the embedding model. See specs/domains/embedded-llm.md.
const EmbeddedModelRelativePath = "models/bonsai-2-27b"

// EmbeddedMLXModelRelativePath is the mtplx engine's MLX weight-pack dir
// within the c0wrk default agent directory, beside — never inside — the llama
// GGUF root, so a scoped removal of one engine's weights can't take the other's.
const EmbeddedMLXModelRelativePath = "models/bonsai-2-27b-mlx"

// WorktreesRelativePath is the repo-relative legacy session-worktree container
// (<repo>/.worktrees/<name>, ADR-080), shared across layers. See specs/domains/session-worktrees.md.
const WorktreesRelativePath = ".worktrees"

// ProjectWorktreesSegment is the directory name of the project-scoped managed
// worktree container (<base>/<projectID>/worktrees/<name>) — deliberately
// distinct from the legacy checkout-local ".worktrees": the project-id segment
// anchors ownership of a DURABLE recorded path independently of the mutable
// repository marker file. See specs/domains/session-worktrees.md.
const ProjectWorktreesSegment = "worktrees"

// GitSafeHooksRelativePath is the agent-dir-relative empty directory every git
// invocation points at via "-c core.hooksPath=<dir>" (internal/sysproc.GitCmd),
// so repository hooks never execute; internal/sysproc keeps a test-pinned
// duplicate (import cycle). See specs/decisions/033-git-subprocess-hardening.md.
const GitSafeHooksRelativePath = "git/safe-hooks"
