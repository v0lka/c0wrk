package backend

import (
	"context"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
)

// appBuilder captures the subset of *core.OrchestratorBuilder used by
// FrontendAPI. The interface lets tests substitute a fake builder so config
// and MCP mutations can be verified without exercising the real LLM router,
// proxy stack, or MCP gateway. (W-20)
//
// *core.OrchestratorBuilder satisfies this interface — see the verification
// at the bottom of this file.
type appBuilder interface {
	RebuildJudge(*core.BuilderConfig)
	RebuildRouter(*core.BuilderConfig) error
	RebuildProxy(context.Context, *core.BuilderConfig) error
	UpdateModelOverrides(*core.BuilderConfig)
	UpdateSearchTool(*core.BuilderConfig)
	UpdateSecurityPolicies(*core.BuilderConfig)
	UpdateShellBlocklist(*core.BuilderConfig) error
	ReconfigureMCP(context.Context, *core.BuilderConfig) error
	ListProviderModels(context.Context, string, *core.BuilderConfig) ([]string, error)
	SetMCPWorkDir(string)
	SetEmbeddedLLM(core.BuilderEmbeddedLLMConfig)
	SetSubscriptionTokenSource(core.BuilderSubscriptionAuthConfig)
	OptimizePrompt(context.Context, string) (*core.OptimizePromptResult, error)
	GenerateCommitMessage(context.Context, string) (string, error)
	GetBaseSkillDirs() []string
	GetSkillDescriptors(projectSkillDir string) []skills.SkillDescriptor
	GetBaseAgentDirs() []string
	GetAgentDescriptors(projectAgentDir string) []agents.AgentDescriptor
	ModelRegistry() *llm.ModelRegistry
	JudgeAvailable() bool
}

// builder returns the appBuilder used by FrontendAPI. Tests inject a fake by
// setting f.builderOverride; production code falls through to the wrapped
// *core.OrchestratorBuilder. Returns nil when neither is available so callers
// must nil-check.
func (f *FrontendAPI) builder() appBuilder {
	if f.builderOverride != nil {
		return f.builderOverride
	}
	// seedAcquire, NOT appCell: builder() is reached from code that already
	// holds configMu (UpdateMCPServers and applyModelProfilesChange under the
	// write lock, GetConfig/ListProviderModels under RLock), and sync.RWMutex
	// is not reentrant — an RLock here would self-deadlock against the held
	// write lock and block behind any queued writer from the read paths.
	// seedAcquire provides the same happens-before edge for the plain f.app
	// read (it is a single atomic load once Init has published) without
	// touching configMu.
	f.seedAcquire()
	if f.app == nil {
		return nil
	}
	return f.app.Builder()
}

// Compile-time assertion that *core.OrchestratorBuilder satisfies appBuilder.
var _ appBuilder = (*core.OrchestratorBuilder)(nil)
