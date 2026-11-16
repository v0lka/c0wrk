package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSDDPath(t *testing.T) {
	got := SDDPath(filepath.Join("home", ".c0wrk"))
	want := filepath.Join("home", ".c0wrk", "specs.yaml")
	if got != want {
		t.Fatalf("SDDPath() = %q, want %q", got, want)
	}
}

func TestProjectSDDPath(t *testing.T) {
	got := ProjectSDDPath(filepath.Join("work", "checkout"))
	want := filepath.Join("work", "checkout", ".c0wrk", "specs.yaml")
	if got != want {
		t.Fatalf("ProjectSDDPath() = %q, want %q", got, want)
	}
}

// TestProjectSDDPath_EmptyWorkspace pins the CWD guard: an empty workspace
// yields "" — never a RELATIVE path. Joining onto "" would produce
// ".c0wrk/specs.yaml", which safeio then resolves against the process CWD,
// reading a stranger's specs.yaml as project data (the sdd.Load skip rule
// only drops empty strings).
func TestProjectSDDPath_EmptyWorkspace(t *testing.T) {
	if got := ProjectSDDPath(""); got != "" {
		t.Fatalf("ProjectSDDPath(\"\") = %q, want \"\"", got)
	}
}

// TestSDDPaths_DoNotMaterialize pins the read-only contract: deriving the paths
// (as every RPC entry point does) creates neither the files nor the
// checkout-local metadata directory around them.
func TestSDDPaths_DoNotMaterialize(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	workspace := filepath.Join(root, "checkout")

	globalPath := SDDPath(agentDir)
	projectPath := ProjectSDDPath(workspace)

	for _, path := range []string{
		globalPath, filepath.Dir(globalPath),
		projectPath, filepath.Dir(projectPath), workspace,
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%q exists, want nothing materialized by the path helpers (stat error = %v)", path, err)
		}
	}
}

// TestProjectSDDPath_OutsideSpecsTree keeps the operator's c0wrk metadata file
// out of the specification tree the specs subsystem parses, so neither
// subsystem can mistake the other's documents for its own.
func TestProjectSDDPath_OutsideSpecsTree(t *testing.T) {
	workspace := filepath.Join("work", "checkout")

	if got, want := filepath.Dir(ProjectSDDPath(workspace)), filepath.Join(workspace, ProjectMetaDirName); got != want {
		t.Fatalf("ProjectSDDPath() parent = %q, want %q", got, want)
	}
	if specsRoot := ProjectSpecsPath(workspace); specsRoot == filepath.Dir(ProjectSDDPath(workspace)) {
		t.Fatalf("ProjectSDDPath() lives in the specs root %q", specsRoot)
	}
	if got, want := ProjectMetaDirName, DefaultAgentDir; got != want {
		t.Fatalf("ProjectMetaDirName = %q, want the agent-dir name %q", got, want)
	}
}
