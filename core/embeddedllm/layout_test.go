package embeddedllm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testModelDirName mirrors the leaf of core.EmbeddedModelRelativePath
// ("models/bonsai-2-27b"), which backend/config.EmbeddedModelDir turns into the
// model root passed to NewLayout. The test cannot import package core — root
// core imports this package, so an import back would cycle — so the name is
// repeated here; backend/config pins the authoritative value in
// TestEmbeddedLLMDirs.
const testModelDirName = "bonsai-2-27b"

// testLayout builds a Layout under a fresh temp agent dir shaped like the real
// one: <agentDir>/runtimes and <agentDir>/models/bonsai-2-27b. The sibling
// tools/bin directory exists so the agent-isolation tests have something to
// assert against.
func testLayout(t *testing.T) (layout Layout, agentDir string) {
	t.Helper()
	agentDir = t.TempDir()
	built, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", testModelDirName),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	layout = built
	if err := os.MkdirAll(filepath.Join(agentDir, "tools", "bin"), 0o750); err != nil {
		t.Fatalf("creating the tools/bin decoy: %v", err)
	}
	return layout, agentDir
}

func TestNewLayoutRejectsUnusableRoots(t *testing.T) {
	t.Parallel()
	abs := t.TempDir()

	cases := []struct {
		name     string
		runtimes string
		model    string
	}{
		{"empty runtimes root", "", filepath.Join(abs, "models", "bonsai-2-27b")},
		{"empty model root", filepath.Join(abs, "runtimes"), ""},
		{"relative runtimes root", "runtimes", filepath.Join(abs, "models", "bonsai-2-27b")},
		{"relative model root", filepath.Join(abs, "runtimes"), "models/bonsai-2-27b"},
		{"identical roots", filepath.Join(abs, "shared"), filepath.Join(abs, "shared")},
		{"model nested in runtimes", filepath.Join(abs, "runtimes"), filepath.Join(abs, "runtimes", "models")},
		{"runtimes nested in model", filepath.Join(abs, "models", "runtimes"), filepath.Join(abs, "models")},
		{"trailing separator only difference", filepath.Join(abs, "a") + string(filepath.Separator), filepath.Join(abs, "a")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewLayout(tc.runtimes, tc.model); !errors.Is(err, ErrLayoutInvalid) {
				t.Fatalf("NewLayout(%q, %q) error = %v, want ErrLayoutInvalid",
					tc.runtimes, tc.model, err)
			}
		})
	}
}

func TestNewLayoutCleansRoots(t *testing.T) {
	t.Parallel()
	abs := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(abs, "runtimes", "..", "runtimes")+string(filepath.Separator),
		filepath.Join(abs, "models", "bonsai-2-27b", "..", "bonsai-2-27b"),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	if want := filepath.Join(abs, "runtimes"); layout.RuntimesRoot != want {
		t.Errorf("RuntimesRoot = %q, want %q", layout.RuntimesRoot, want)
	}
	if want := filepath.Join(abs, "models", "bonsai-2-27b"); layout.ModelRoot != want {
		t.Errorf("ModelRoot = %q, want %q", layout.ModelRoot, want)
	}
}

func TestRuntimeDirNameCarriesTagAndBackend(t *testing.T) {
	t.Parallel()
	cases := map[Backend]string{
		BackendMetal:   "llama-" + RuntimeTag + "-metal",
		BackendCUDA124: "llama-" + RuntimeTag + "-cuda-12.4",
		BackendCPU:     "llama-" + RuntimeTag + "-cpu",
		BackendVulkan:  "llama-" + RuntimeTag + "-vulkan",
		BackendROCm:    "llama-" + RuntimeTag + "-rocm",
		BackendCUDA133: "llama-" + RuntimeTag + "-cuda-13.3",
		BackendCUDA128: "llama-" + RuntimeTag + "-cuda-12.8",
	}
	for backend, want := range cases {
		if got := RuntimeDirName(backend); got != want {
			t.Errorf("RuntimeDirName(%q) = %q, want %q", backend, got, want)
		}
	}
	// The documented on-disk shape: ~/.c0wrk/runtimes/llama-<tag>-<backend>/.
	if got := RuntimeDirName(BackendMetal); !strings.HasPrefix(got, "llama-"+RuntimeTag+"-") {
		t.Errorf("RuntimeDirName(%q) = %q, does not match llama-<tag>-<backend>", BackendMetal, got)
	}
}

func TestLayoutDerivedPaths(t *testing.T) {
	t.Parallel()
	layout, agentDir := testLayout(t)

	runtimes := filepath.Join(agentDir, "runtimes")
	models := filepath.Join(agentDir, "models", testModelDirName)

	checks := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"runtime dir", func() (string, error) { return layout.RuntimeDir(BackendMetal) },
			filepath.Join(runtimes, "llama-"+RuntimeTag+"-metal")},
		{"staging dir", func() (string, error) { return layout.RuntimeStagingDir(BackendMetal) },
			filepath.Join(runtimes, "llama-"+RuntimeTag+"-metal.staging")},
		{"retired dir", func() (string, error) { return layout.RuntimeRetiredDir(BackendMetal) },
			filepath.Join(runtimes, "llama-"+RuntimeTag+"-metal.old")},
		{"downloads dir", layout.DownloadsDir, filepath.Join(runtimes, "downloads")},
		{"manifest", layout.ManifestPath, filepath.Join(models, "manifest.json")},
		{"model file", func() (string, error) { return layout.ModelFile(PackingPQ2_0) },
			filepath.Join(models, modelFilePQ2_0)},
		{"model file (vulkan packing)", func() (string, error) { return layout.ModelFile(PackingPTQ1_0) },
			filepath.Join(models, modelFilePTQ1_0)},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.got()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestLayoutDestinationRoutesByComponent(t *testing.T) {
	t.Parallel()
	layout, agentDir := testLayout(t)

	runtimes := filepath.Join(agentDir, "runtimes")
	models := filepath.Join(agentDir, "models", testModelDirName)

	cases := []struct {
		asset Asset
		want  string
	}{
		// Runtime archives are staged: the archive is not the artifact.
		{Asset{Component: ComponentRuntime, ArchiveName: "llama-x.tar.gz"},
			filepath.Join(runtimes, "downloads", "llama-x.tar.gz")},
		{Asset{Component: ComponentCudart, ArchiveName: "cudart-x.zip"},
			filepath.Join(runtimes, "downloads", "cudart-x.zip")},
		// Weights are downloaded straight to their final path: the artifact IS
		// the file, and the Downloader promotes it only after verification.
		{Asset{Component: ComponentModel, ArchiveName: modelFilePQ2_0},
			filepath.Join(models, modelFilePQ2_0)},
		{Asset{Component: ComponentMMProj, ArchiveName: mmprojFileQ8_0},
			filepath.Join(models, mmprojFileQ8_0)},
	}
	for _, tc := range cases {
		got, err := layout.Destination(tc.asset)
		if err != nil {
			t.Fatalf("Destination(%s): %v", tc.asset.Component, err)
		}
		if got != tc.want {
			t.Errorf("Destination(%s) = %q, want %q", tc.asset.Component, got, tc.want)
		}
	}

	if _, err := layout.Destination(Asset{Component: "unknown", ArchiveName: "x"}); !errors.Is(err, ErrLayoutInvalid) {
		t.Errorf("Destination(unknown component) error = %v, want ErrLayoutInvalid", err)
	}
}

// TestLayoutRejectsEscapingNames proves the defence-in-depth gate: an asset
// name that would leave the tree is refused instead of joined. Containment is
// decided by pathutil, never by an inline prefix comparison.
func TestLayoutRejectsEscapingNames(t *testing.T) {
	t.Parallel()
	layout, agentDir := testLayout(t)

	escapes := []string{
		"../escape.gguf",
		"../../escape.gguf",
		filepath.Join("..", "runtimes", "escape"),
		"..",
	}
	for _, name := range escapes {
		for _, component := range []Component{ComponentModel, ComponentRuntime} {
			_, err := layout.Destination(Asset{Component: component, ArchiveName: name})
			if !errors.Is(err, ErrLayoutInvalid) {
				t.Errorf("Destination(%s, %q) error = %v, want ErrLayoutInvalid", component, name, err)
			}
		}
	}

	// An empty segment is refused too: it would silently address the root.
	if _, err := layout.Destination(Asset{Component: ComponentModel}); !errors.Is(err, ErrLayoutInvalid) {
		t.Errorf("Destination(empty name) error = %v, want ErrLayoutInvalid", err)
	}

	// The tools/bin tree the agent's PATH exposes is outside the layout, so no
	// derived path can ever point into it.
	toolsBin := filepath.Join(agentDir, "tools", "bin")
	if layout.Owns(toolsBin) || layout.Owns(filepath.Join(toolsBin, "llama-server")) {
		t.Errorf("Owns() accepted a path under %q — the agent PATH directory must never be owned", toolsBin)
	}
}

func TestLayoutOwns(t *testing.T) {
	t.Parallel()
	layout, agentDir := testLayout(t)

	runtimeDir, err := layout.RuntimeDir(BackendCPU)
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	modelFile, err := layout.ModelFile(PackingPQ2_0)
	if err != nil {
		t.Fatalf("ModelFile: %v", err)
	}

	owned := []string{
		layout.RuntimesRoot,
		layout.ModelRoot,
		runtimeDir,
		filepath.Join(runtimeDir, "build", "bin", "llama-server"),
		modelFile,
		filepath.Join(layout.ModelRoot, "manifest.json"),
	}
	for _, p := range owned {
		if !layout.Owns(p) {
			t.Errorf("Owns(%q) = false, want true", p)
		}
	}

	notOwned := []string{
		"",
		agentDir,
		filepath.Join(agentDir, "models"), // the embedding-model root
		filepath.Join(agentDir, "models", "ggml-model-q4_0.gguf"), // a flat embedding model
		filepath.Join(agentDir, "tools", "bin", "rg"),             // the agent PATH
		filepath.Join(agentDir, "config.yaml"),
		"/etc/passwd",
	}
	for _, p := range notOwned {
		if layout.Owns(p) {
			t.Errorf("Owns(%q) = true, want false", p)
		}
	}
}

func TestLayoutZeroValueOwnsNothing(t *testing.T) {
	t.Parallel()
	var layout Layout
	for _, p := range []string{"", "/", "/tmp/anything", "relative"} {
		if layout.Owns(p) {
			t.Errorf("zero Layout.Owns(%q) = true, want false", p)
		}
	}
	if err := layout.EnsureRoots(); !errors.Is(err, ErrLayoutInvalid) {
		t.Errorf("zero Layout.EnsureRoots() error = %v, want ErrLayoutInvalid", err)
	}
	if _, err := layout.ManifestPath(); !errors.Is(err, ErrLayoutInvalid) {
		t.Errorf("zero Layout.ManifestPath() error = %v, want ErrLayoutInvalid", err)
	}
}

func TestLayoutEnsureRoots(t *testing.T) {
	t.Parallel()
	layout, _ := testLayout(t)

	if err := layout.EnsureRoots(); err != nil {
		t.Fatalf("EnsureRoots: %v", err)
	}
	downloads, err := layout.DownloadsDir()
	if err != nil {
		t.Fatalf("DownloadsDir: %v", err)
	}
	for _, dir := range []string{layout.RuntimesRoot, layout.ModelRoot, downloads} {
		info, serr := os.Stat(dir)
		if serr != nil {
			t.Fatalf("stat %q: %v", dir, serr)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}
	// Idempotent: a second call is a no-op, not an error.
	if err := layout.EnsureRoots(); err != nil {
		t.Fatalf("second EnsureRoots: %v", err)
	}
}

func TestServerBinaryPathFindsNestedBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "build", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	binary := filepath.Join(binDir, "llama-server")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A decoy that must not be picked: same prefix, wrong name.
	if err := os.WriteFile(filepath.Join(binDir, "llama-cli"), []byte("x"), 0o755); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	got, err := ServerBinaryPath(root, "linux")
	if err != nil {
		t.Fatalf("ServerBinaryPath: %v", err)
	}
	if got != binary {
		t.Errorf("ServerBinaryPath = %q, want %q", got, binary)
	}
}

func TestServerBinaryPathWindowsSuffix(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build", "bin"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := filepath.Join(root, "build", "bin", "llama-server.exe")
	if err := os.WriteFile(want, []byte("MZ"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := ServerBinaryPath(root, "windows")
	if err != nil {
		t.Fatalf("ServerBinaryPath(windows): %v", err)
	}
	if got != want {
		t.Errorf("ServerBinaryPath(windows) = %q, want %q", got, want)
	}
}

func TestServerBinaryPathRefusesMissingBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "build", "bin"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := ServerBinaryPath(root, "linux")
	if err == nil {
		t.Fatal("ServerBinaryPath on a tree without llama-server succeeded, want an error")
	}
	if !strings.Contains(err.Error(), ServerBinaryName) {
		t.Errorf("error %q does not name the missing binary", err)
	}
	if _, err := ServerBinaryPath("", "linux"); err == nil {
		t.Error("ServerBinaryPath(\"\") succeeded, want an error")
	}
	if _, err := ServerBinaryPath(filepath.Join(root, "absent"), "linux"); err == nil {
		t.Error("ServerBinaryPath on a missing directory succeeded, want an error")
	}
}

func TestNeedsAdHocSignature(t *testing.T) {
	t.Parallel()
	yes := []string{"llama-server", "llama-server.exe", "llama-cli", "libggml.dylib", "libllama.dylib"}
	for _, name := range yes {
		if !needsAdHocSignature(name) {
			t.Errorf("needsAdHocSignature(%q) = false, want true", name)
		}
	}
	no := []string{"README.md", "ggml-metal.metallib", "libc++.so", "server", "metal_helper"}
	for _, name := range no {
		if needsAdHocSignature(name) {
			t.Errorf("needsAdHocSignature(%q) = true, want false", name)
		}
	}
}
