package core

import "testing"

// TestNoProjectDisabledTools_OnlySemanticSearch pins the No Project (CHAT)
// tool policy: semantic_search is the ONLY tool disabled — without a project
// there is no vector index, so the tool would either fail or return stale
// results from the previous CODE-mode project. ripgrep and glob were
// re-enabled for non-code CHAT work (OS config, DevOps, dotfiles) and must
// not silently regress into the disabled set. The same set drives both the
// LLM tool listing (ListFiltered) and the execution gate, so pinning the
// variable pins the whole policy.
func TestNoProjectDisabledTools_OnlySemanticSearch(t *testing.T) {
	if len(NoProjectDisabledTools) != 1 || !NoProjectDisabledTools[ToolSemanticSearch] {
		t.Fatalf("NoProjectDisabledTools must be exactly {semantic_search: true}, got %v", NoProjectDisabledTools)
	}
	for _, name := range []string{
		ToolRipgrep, ToolGlob, ToolReadFile, ToolListDirectory,
		ToolWriteFile, ToolEditFile, ToolBashExec, ToolWebSearch, ToolWebFetch,
	} {
		if NoProjectDisabledTools[name] {
			t.Errorf("tool %q must NOT be disabled in No Project (CHAT) mode", name)
		}
	}
}
