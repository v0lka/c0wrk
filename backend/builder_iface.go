package backend

import (
	"context"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/skills"
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
	UpdateSearchTool(*core.BuilderConfig)
	UpdateSecurityPolicies(*core.BuilderConfig)
	ReconfigureMCP(context.Context, *core.BuilderConfig) error
	ListProviderModels(context.Context, string, *core.BuilderConfig) ([]string, error)
	SetMCPWorkDir(string)
	OptimizePrompt(context.Context, string) (*core.OptimizePromptResult, error)
	GetBaseSkillDirs() []string
	GetSkillDescriptors(projectSkillDir string) []skills.SkillDescriptor
	ModelRegistry() *llm.ModelRegistry
}

// builder returns the appBuilder used by FrontendAPI. Tests inject a fake by
// setting f.builderOverride; production code falls through to the wrapped
// *core.OrchestratorBuilder. Returns nil when neither is available so callers
// must nil-check.
func (f *FrontendAPI) builder() appBuilder {
	if f.builderOverride != nil {
		return f.builderOverride
	}
	if f.app == nil {
		return nil
	}
	return f.app.Builder()
}

// Compile-time assertion that *core.OrchestratorBuilder satisfies appBuilder.
var _ appBuilder = (*core.OrchestratorBuilder)(nil)
