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
