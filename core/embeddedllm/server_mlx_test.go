package embeddedllm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func probeTimeoutForTest() time.Duration { return 2 * time.Second }

func mlxLaunchSpec() LaunchSpec {
	return LaunchSpec{
		Engine:       EngineMTPLX,
		ServerBinary: "/runtimes/mtplx-" + MTPLXVersion + "/python/bin/python3.12",
		ModelFile:    "/models/bonsai-2-27b-mlx/pack",
		Host:         LoopbackHost,
		Port:         18432,
		ContextSize:  94208,
	}
}

// TestMLXArgsRendersTheEngineContract locks the mtplx argv: the isolated
// interpreter, the module entry point and `serve` subcommand (verified against
// the pinned wheel: no mtplx/__main__.py exists, and `serve` spells the model
// --model with no -m alias), fixed order, typed values, loopback bind,
// explicit window, rate limiting off, and the model given as the verified pack
// directory.
func TestMLXArgsRendersTheEngineContract(t *testing.T) {
	spec := mlxLaunchSpec()
	// The deliberate split (see the Args rendering guard): Args is ALWAYS the
	// llama renderer; spawn selects mlxArgs by spec.Engine. Assert both sides.
	if spec.Args() == nil || len(spec.Args()) == 0 {
		t.Fatal("Args rendered nothing")
	}
	if strings.Join(spec.Args(), " ") == strings.Join(spec.mlxArgs(), " ") {
		t.Fatal("the llama and mtplx renderers coincide; the selector is meaningless")
	}
	argv := spec.mlxArgs()
	want := []string{
		"-I",
		"-m", "mtplx.cli", "serve",
		"--model", spec.ModelFile,
		"--host", LoopbackHost,
		"--port", "18432",
		"--context-window", "94208",
		"--rate-limit", "0",
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %v)", i, argv[i], want[i], argv)
		}
	}
	// The dash-prefixed set is exactly the flags the engine contract chose —
	// nothing a hostile ModelFile string could add.
	dashes := 0
	for _, a := range argv {
		if strings.HasPrefix(a, "-") {
			dashes++
		}
	}
	if dashes != 7 {
		t.Fatalf("argv carries %d flags, want exactly 7 (-I, -m, --model, host, port, context-window, rate-limit)", dashes)
	}
}

func TestMLXValidate(t *testing.T) {
	good := mlxLaunchSpec()
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*LaunchSpec)
		wantSub string
	}{
		{"no pack dir", func(s *LaunchSpec) { s.ModelFile = "" }, "pack directory is required"},
		{"relative pack dir", func(s *LaunchSpec) { s.ModelFile = "models/pack" }, "must be absolute"},
		{"non-loopback bind", func(s *LaunchSpec) { s.Host = "0.0.0.0" }, "loopback"},
		{"bad port", func(s *LaunchSpec) { s.Port = 0 }, "usable TCP port"},
		{"zero context", func(s *LaunchSpec) { s.ContextSize = 0 }, "context size"},
		{"over-training context", func(s *LaunchSpec) { s.ContextSize = maxTrainingContext + 1 }, "context size"},
		{"fit leaked from llama", func(s *LaunchSpec) { s.Fit = true }, "llama-family launch fields"},
		{"mmproj leaked from llama", func(s *LaunchSpec) { s.MMProjFile = "/x/mmproj.gguf" }, "llama-family launch fields"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := mlxLaunchSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Validate error = %v, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

func TestLaunchSpecMLX(t *testing.T) {
	agentDir := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", "bonsai-2-27b"),
		filepath.Join(agentDir, "models", "bonsai-2-27b-mlx"),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	// A provisioned tree: the interpreter and the pack both exist.
	runtimeDir, err := layout.MLXRuntimeDir()
	if err != nil {
		t.Fatalf("MLXRuntimeDir: %v", err)
	}
	python, err := MLXServerPythonPath(runtimeDir)
	if err != nil {
		t.Fatalf("MLXServerPythonPath: %v", err)
	}
	if err := mkdirAll(filepath.Dir(python)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatalf("touch python: %v", err)
	}
	packDir, err := layout.MLXPackDir()
	if err != nil {
		t.Fatalf("MLXPackDir: %v", err)
	}
	if err := mkdirAll(packDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	server := NewServer(layout, discardLogger())
	ctx := context.Background()

	// The recorded plan is the shape: its context is the window.
	plan := MemoryPlan{ContextSize: 20480}
	resolved, err := server.launchSpec(ctx, Manifest{
		Engine:      EngineMTPLX,
		ModelFile:   packDir,
		ContextSize: 20480,
		Plan:        &plan,
	}, 18432)
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	if resolved.Spec.Engine != EngineMTPLX || resolved.Spec.ContextSize != 20480 {
		t.Fatalf("spec = %+v", resolved.Spec)
	}
	if resolved.Spec.ServerBinary != python {
		t.Fatalf("ServerBinary = %q, want %q", resolved.Spec.ServerBinary, python)
	}
	if resolved.Spec.ModelFile != packDir {
		t.Fatalf("ModelFile = %q, want the pack dir", resolved.Spec.ModelFile)
	}

	// A tampered record pointing outside the pack root falls back to the
	// layout-derived directory, loudly.
	resolved, err = server.launchSpec(ctx, Manifest{
		Engine:      EngineMTPLX,
		ModelFile:   "/elsewhere/pack",
		ContextSize: 8192,
	}, 18432)
	if err != nil {
		t.Fatalf("launchSpec(tampered): %v", err)
	}
	if resolved.Spec.ModelFile != packDir {
		t.Fatalf("tampered ModelFile = %q, want the derived %q", resolved.Spec.ModelFile, packDir)
	}

	// A plan-less manifest launches the recorded context tier.
	resolved, err = server.launchSpec(ctx, Manifest{
		Engine:      EngineMTPLX,
		ModelFile:   packDir,
		ContextSize: 8192,
	}, 18432)
	if err != nil {
		t.Fatalf("launchSpec(plan-less): %v", err)
	}
	if resolved.Spec.ContextSize != 8192 {
		t.Fatalf("plan-less context = %d, want the recorded 8192", resolved.Spec.ContextSize)
	}

	// A record with NO context anywhere is a refusal, not a guess.
	_, err = server.launchSpec(ctx, Manifest{Engine: EngineMTPLX, ModelFile: packDir}, 18432)
	if !errors.Is(err, ErrLaunchSpecInvalid) {
		t.Fatalf("launchSpec(no context) error = %v, want ErrLaunchSpecInvalid", err)
	}
}

// TestMLXChildEnv locks the mtplx python children's complete environment: the
// operator's PIP_*/PYTHON*/MTPLX_* influence is scrubbed, the pinned bin dir
// leads PATH, user site-packages are off, temp passes through, and the
// engine's own state is pointed inside the tree — set once, never inherited
// as a duplicate a C reader could resolve first. The fixture paths are built
// with the host's filepath — the function's dialect (it receives the real
// staged-tree paths in production) — so the scrub/shape verdicts are provable
// on every CI machine; only the PATH list separator is a goos decision.
func TestMLXChildEnv(t *testing.T) {
	parent := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"PIP_FIND_LINKS=/operator/mirror",
		"PIP_INDEX_URL=https://operator.example/simple",
		"PYTHONPATH=/operator/site-packages",
		"PYTHONHOME=/operator/python",
		"MTPLX_CONFIG=/operator/.mtplx/config.toml",
		"MTPLX_MODEL_DIR=/operator/.mtplx",
		"TMPDIR=/var/folders/tmp",
		"HOME=/Users/operator",
	}
	python := string(filepath.Separator) + filepath.Join("runtimes", "mtplx-"+MTPLXVersion, "python", "bin", "python3.12")
	treeRoot := filepath.Dir(filepath.Dir(filepath.Dir(python)))
	binDir := filepath.Dir(python)
	env := mlxChildEnv(parent, "darwin", python)

	byName := map[string]string{}
	for _, entry := range env {
		name, value, found := strings.Cut(entry, "=")
		if found {
			byName[name] = value
		}
	}
	// The scrub: nothing the operator steers with survives.
	for _, banned := range []string{"PIP_FIND_LINKS", "PIP_INDEX_URL", "PYTHONPATH", "PYTHONHOME"} {
		if _, ok := byName[banned]; ok {
			t.Fatalf("child env carries inherited %s=%q", banned, byName[banned])
		}
	}
	// The MTPLX_* keys are SET, not inherited: the operator's values are gone.
	if byName["MTPLX_CONFIG"] == "/operator/.mtplx/config.toml" || byName["MTPLX_MODEL_DIR"] == "/operator/.mtplx" {
		t.Fatal("child env resolved the operator's MTPLX_* values")
	}
	// The pinned bin dir FIRST on PATH, the operator's tail kept; the list
	// separator is the declared goos's (darwin), a pure string choice.
	if want := binDir + pathListSeparator("darwin") + "/usr/local/bin:/usr/bin:/bin"; byName["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", byName["PATH"], want)
	}
	if byName["PYTHONNOUSERSITE"] != "1" {
		t.Fatalf("PYTHONNOUSERSITE = %q, want 1 (the user site-packages shadow)", byName["PYTHONNOUSERSITE"])
	}
	// Temp passes through; unrelated variables pass through.
	if byName["TMPDIR"] != "/var/folders/tmp" {
		t.Fatalf("TMPDIR = %q, want it passed through", byName["TMPDIR"])
	}
	if byName["HOME"] != "/Users/operator" {
		t.Fatalf("HOME = %q, want it passed through", byName["HOME"])
	}
	// The engine's state is set exactly once, to the tree's own paths.
	if want := filepath.Join(treeRoot, "config.toml"); byName["MTPLX_CONFIG"] != want {
		t.Fatalf("MTPLX_CONFIG = %q, want %q", byName["MTPLX_CONFIG"], want)
	}
	if want := treeRoot; byName["MTPLX_MODEL_DIR"] != want {
		t.Fatalf("MTPLX_MODEL_DIR = %q, want %q", byName["MTPLX_MODEL_DIR"], want)
	}
	for _, entry := range env {
		if strings.Count(entry, "MTPLX_") > 0 && strings.HasPrefix(entry, "MTPLX_") {
			if got := strings.SplitN(entry, "=", 2)[0]; got != "MTPLX_CONFIG" && got != "MTPLX_MODEL_DIR" {
				t.Fatalf("unexpected MTPLX key %q in child env", got)
			}
		}
	}
	// A parent without PATH still yields a working child.
	bare := mlxChildEnv([]string{"HOME=/Users/operator"}, "darwin", python)
	joined := strings.Join(bare, "\n")
	if !strings.Contains(joined, "PATH="+binDir+"\n") {
		t.Fatalf("PATH-less parent produced %q without the pinned bin dir", joined)
	}
}

func TestReadHealthContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok": true, "context_window": 57344}`))
	}))
	defer srv.Close()

	client := srv.Client()
	got, ok := readHealthContext(context.Background(), client, srv.URL+"/health", probeTimeoutForTest())
	if !ok || got != 57344 {
		t.Fatalf("readHealthContext = (%d, %v), want (57344, true)", got, ok)
	}

	// Fail-soft on every malformed answer. The fixtures carry the field the
	// parser ACTUALLY reads (the top-level `context_window`), so every guard
	// below — nil pointer, non-positive, over-training-context, type error —
	// is exercised on the real field, not on a field the parser ignores.
	for name, body := range map[string]string{
		"missing field": `{"ok": true}`,
		"zero":          `{"context_window": 0}`,
		"negative":      `{"context_window": -5}`,
		"absurd":        `{"context_window": 999999999}`,
		"not json":      `hello`,
		"wrong type":    `{"context_window": "big"}`,
	} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		if _, ok := readHealthContext(context.Background(), bad.Client(), bad.URL+"/health", probeTimeoutForTest()); ok {
			t.Fatalf("%s: readHealthContext accepted %q", name, body)
		}
		bad.Close()
	}

	// The boundary is the model's training context: the value itself is
	// accepted, one token more is refused.
	boundary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"context_window": ` + strconv.Itoa(maxTrainingContext) + `}`))
	}))
	defer boundary.Close()
	got, ok = readHealthContext(context.Background(), boundary.Client(), boundary.URL+"/health", probeTimeoutForTest())
	if !ok || got != maxTrainingContext {
		t.Fatalf("readHealthContext(maxTrainingContext) = (%d, %v), want (%d, true)", got, ok, maxTrainingContext)
	}
	over := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"context_window": ` + strconv.Itoa(maxTrainingContext+1) + `}`))
	}))
	defer over.Close()
	if _, ok := readHealthContext(context.Background(), over.Client(), over.URL+"/health", probeTimeoutForTest()); ok {
		t.Fatal("readHealthContext accepted maxTrainingContext+1")
	}

	if _, ok := readHealthContext(context.Background(), client, "http://127.0.0.1:1/health", probeTimeoutForTest()); ok {
		t.Fatal("a dead server answered the readback")
	}
}

// mlxLaunchTestServer builds a Server over a provisioned-shape layout: the
// interpreter and the pack directory both exist, as a launch expects.
func mlxLaunchTestServer(t *testing.T) (*Server, Layout, string) {
	t.Helper()
	agentDir := t.TempDir()
	layout, err := NewLayout(
		filepath.Join(agentDir, "runtimes"),
		filepath.Join(agentDir, "models", "bonsai-2-27b"),
		filepath.Join(agentDir, "models", "bonsai-2-27b-mlx"),
	)
	if err != nil {
		t.Fatalf("NewLayout: %v", err)
	}
	runtimeDir, err := layout.MLXRuntimeDir()
	if err != nil {
		t.Fatalf("MLXRuntimeDir: %v", err)
	}
	python, err := MLXServerPythonPath(runtimeDir)
	if err != nil {
		t.Fatalf("MLXServerPythonPath: %v", err)
	}
	if err := mkdirAll(filepath.Dir(python)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(python, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatalf("touch python: %v", err)
	}
	packDir, err := layout.MLXPackDir()
	if err != nil {
		t.Fatalf("MLXPackDir: %v", err)
	}
	if err := mkdirAll(packDir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return NewServer(layout, discardLogger()), layout, packDir
}

// TestLaunchSpecMLXLiveReplan keeps `context.mode: exact` alive under the
// mtplx engine (ADR-092 D5): the launch re-plans with the LIVE operator
// tuning from the recorded topology snapshot, refuses a window the machine
// cannot hold with the gate's own error, and falls back to the recorded plan
// when no pricing is possible.
func TestLaunchSpecMLXLiveReplan(t *testing.T) {
	server, _, packDir := mlxLaunchTestServer(t)
	ctx := context.Background()
	recorded := MemoryPlan{ContextSize: 94208}
	manifest := Manifest{
		Engine:    EngineMTPLX,
		ModelFile: packDir,
		Port:      18432,
		Plan:      &recorded,
		Topology:  &MemoryTopology{HostRAMGiB: 24, Unified: true},
	}

	// An exact override REPLACES the recorded window.
	server.Tuning = func() Tuning {
		return Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: 12345}}
	}
	resolved, err := server.launchSpec(ctx, manifest, 18432)
	if err != nil {
		t.Fatalf("launchSpec(exact): %v", err)
	}
	if resolved.Spec.ContextSize != 12345 {
		t.Fatalf("exact override context = %d, want 12345", resolved.Spec.ContextSize)
	}

	// A window the machine cannot hold refuses the launch with the gate's
	// own arithmetic — it never silently launches the smaller recorded plan.
	server.Tuning = func() Tuning {
		return Tuning{Context: ContextTuning{Mode: ContextExact, Tokens: maxTrainingContext}}
	}
	_, err = server.launchSpec(ctx, manifest, 18432)
	if !errors.Is(err, ErrMemoryPlanInfeasible) {
		t.Fatalf("launchSpec(infeasible exact) error = %v, want ErrMemoryPlanInfeasible", err)
	}

	// A llama-only tuning key refuses the load fail-closed: config validation
	// is skipped when `engine: auto` resolves to mtplx, so the launch is the
	// last checkpoint — launching the recorded plan would silently ignore the
	// key.
	for key, tuning := range map[string]Tuning{
		"kv_cache_type": {KVType: "q8_0"},
		"devices":       {Devices: []string{"MTL0"}},
		"split_mode":    {SplitMode: SplitModeRow},
	} {
		server.Tuning = func() Tuning { return tuning }
		_, err = server.launchSpec(ctx, manifest, 18432)
		if !errors.Is(err, ErrTuningInvalid) {
			t.Fatalf("launchSpec(forbidden %s) error = %v, want it to wrap ErrTuningInvalid", key, err)
		}
		if msg := err.Error(); !strings.Contains(msg, key) {
			t.Errorf("forbidden-%s error message %q does not name the key", key, msg)
		}
	}

	// No topology snapshot (a pre-snapshot manifest) leaves the recorded plan
	// in place: ErrRAMUnknown falls back, never fails.
	server.Tuning = func() Tuning { return Tuning{} }
	noSnapshot := manifest
	noSnapshot.Topology = nil
	resolved, err = server.launchSpec(ctx, noSnapshot, 18432)
	if err != nil {
		t.Fatalf("launchSpec(no topology): %v", err)
	}
	if resolved.Spec.ContextSize != 94208 {
		t.Fatalf("no-topology fallback context = %d, want the recorded 94208", resolved.Spec.ContextSize)
	}
}

func TestModelsReadyWindowPredicate(t *testing.T) {
	twentyK := 20480
	// llama shape: id only, no window fields — ready without the requirement.
	if !modelsReady(modelsPayload{Data: []struct {
		ID            string `json:"id"`
		ContextLength *int   `json:"context_length"`
		MaxContextLen *int   `json:"max_context_length"`
		MaxModelLen   *int   `json:"max_model_len"`
	}{{ID: "llama"}}}, false) {
		t.Fatal("llama shape refused without the requirement")
	}
	// mtplx shape before the backend resolved: id listed, no window yet.
	noWindow := modelsPayload{Data: []struct {
		ID            string `json:"id"`
		ContextLength *int   `json:"context_length"`
		MaxContextLen *int   `json:"max_context_length"`
		MaxModelLen   *int   `json:"max_model_len"`
	}{{ID: "mtplx-bonsai-2-27b-optimized-speed"}}}
	if modelsReady(noWindow, true) {
		t.Fatal("a windowless listing passed the mtplx readiness predicate")
	}
	withWindow := modelsPayload{Data: []struct {
		ID            string `json:"id"`
		ContextLength *int   `json:"context_length"`
		MaxContextLen *int   `json:"max_context_length"`
		MaxModelLen   *int   `json:"max_model_len"`
	}{{ID: "mtplx-bonsai-2-27b-optimized-speed", ContextLength: &twentyK}}}
	if !modelsReady(withWindow, true) {
		t.Fatal("a windowed listing failed the mtplx readiness predicate")
	}
}

func TestScanMLXFitFailure(t *testing.T) {
	msg := scanMLXFitFailure("boot log\nMODEL DOES NOT FIT\nmore log")
	if msg == "" {
		t.Fatal("the admission refusal marker was not recognized")
	}
	if scanFitFailure("MODEL DOES NOT FIT") == "" {
		t.Fatal("the shared scanner does not carry the mtplx marker")
	}
	if scanFitFailure("an ordinary boot log") != "" {
		t.Fatal("an ordinary log was reported as a fit failure")
	}
}
