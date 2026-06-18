package core

import (
	"testing"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

func TestFilterPlannerPromptTools(t *testing.T) {
	// Build a descriptor list containing both excluded and allowed tools.
	all := []sdktools.ToolDescriptor{
		{Name: ToolFinish},
		{Name: ToolAskUser},
		{Name: ToolSetStepStatus},
		{Name: ToolReadStepOutput},
		{Name: ToolListStepOutput},
		{Name: ToolToolResultRead},
		{Name: ToolReadSkillRes},
		{Name: ToolReadFile},
		{Name: ToolListDirectory},
		{Name: ToolBashExec},
		{Name: ToolWriteFile},
		{Name: "some_unknown_tool"},
	}

	result := filterPlannerPromptTools(all)

	resultNames := make(map[string]bool, len(result))
	for _, t := range result {
		resultNames[t.Name] = true
	}

	// Every excluded tool must NOT appear in the result.
	for name := range plannerPromptExcludedTools {
		if resultNames[name] {
			t.Errorf("excluded tool %q appeared in filtered result", name)
		}
	}

	// Known plan-level tools must pass through.
	wantPresent := []string{ToolReadFile, ToolListDirectory, ToolBashExec, ToolWriteFile}
	for _, name := range wantPresent {
		if !resultNames[name] {
			t.Errorf("expected plan tool %q to pass through, but it was filtered out", name)
		}
	}

	// Unknown tools pass through (conservative: only known-internal tools are removed).
	if !resultNames["some_unknown_tool"] {
		t.Errorf("unknown tool %q was unexpectedly filtered out", "some_unknown_tool")
	}
}
