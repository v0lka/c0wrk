package backend

import (
	"context"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
)

// appBuilder captures the subset of *core.OrchestratorBuilder used by FrontendAPI
// so tests can substitute a fake builder without the real LLM router, proxy stack,
// or MCP gateway; the compile-time assertion below pins the real builder.
type appBuilder interface {
	RebuildJudge(*core.BuilderConfig)
	RebuildRouter(*core.BuilderConfig) error
	RebuildProxy(context.Context, *core.BuilderConfig) error
	UpdateModelOverrides(*core.BuilderConfig)
	UpdateSearchTool(*core.BuilderConfig) error
	UpdateSecurityPolicies(*core.BuilderConfig)
	UpdateCapabilities(*core.BuilderConfig)
	SetVectorSearchEnabled(enabled bool)
	UpdateShellBlocklist(*core.BuilderConfig) error
	ReconfigureMCP(context.Context, *core.BuilderConfig) error
	ListProviderModels(context.Context, string, *core.BuilderConfig) ([]string, error)
	SetMCPWorkDir(string)
	SetEmbeddedLLM(core.BuilderEmbeddedLLMConfig)
	SetSubscriptionTokenSource(core.BuilderSubscriptionAuthConfig)
	OptimizePrompt(context.Context, string) (*core.OptimizePromptResult, error)
	GenerateCommitMessage(context.Context, string) (string, error)
	GetBaseSkillDirs() []string
	// SkillDirsForWorkspace returns the complete ordered skill discovery set for
	// one session workspace (project-local + SDD framework dirs + base dirs).
	// sddCheckoutPath is the project's repository checkout whose checkout-local
	// SDD operator DSL merges into the detection catalog (never the session
	// tree); an empty value falls back to workspacePath.
	SkillDirsForWorkspace(workspacePath, sddCheckoutPath string) []string
	// GetSkillDescriptors scans an explicit discovery set (the same set
	// SkillDirsForWorkspace returns).
	GetSkillDescriptors(dirs []string) []skills.SkillDescriptor
	// ResolveSkillResourcePath resolves a read_skill_resource (skill, path)
	// call over the workspace's FULL ordered discovery set (the same set
	// SkillDirsForWorkspace returns for workspacePath — "" in CHAT mode).
	ResolveSkillResourcePath(workspacePath, skillName, relPath string) (string, error)
	GetBaseAgentDirs() []string
	GetAgentDescriptors(projectAgentDir string) []agents.AgentDescriptor
	ModelRegistry() *llm.ModelRegistry
	JudgeAvailable() bool
}

// builder returns the appBuilder used by FrontendAPI: f.builderOverride in tests,
// otherwise the wrapped *core.OrchestratorBuilder. Nil when neither exists, so
// callers must nil-check.
func (f *FrontendAPI) builder() appBuilder {
	if f.builderOverride != nil {
		return f.builderOverride
	}
	// seedAcquire, NOT appCell: callers already hold configMu (write- and RLock
	// paths alike) and sync.RWMutex is not reentrant — an RLock would self-deadlock.
	// seedAcquire gives the same happens-before edge for the plain f.app read.
	f.seedAcquire()
	if f.app == nil {
		return nil
	}
	// Box only a NON-nil concrete builder: a nil *OrchestratorBuilder assigned
	// to the interface would be a typed nil (≠ nil interface) and slip past
	// every caller's nil-check, panicking on the first receiver dereference.
	if b := f.app.Builder(); b != nil {
		return b
	}
	return nil
}

// Compile-time assertion that *core.OrchestratorBuilder satisfies appBuilder.
var _ appBuilder = (*core.OrchestratorBuilder)(nil)
