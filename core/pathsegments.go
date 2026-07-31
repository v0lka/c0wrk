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
