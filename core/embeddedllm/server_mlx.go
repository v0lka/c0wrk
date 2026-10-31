package embeddedllm

// The mtplx engine's supervision surface (ADR-092): argv, validation, launch
// identity, child environment and the /health context readback. The supervisor
// machinery itself is runtime-agnostic and lives in server.go; see
// specs/domains/embedded-llm.md (Load / serve / unload).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── argv ─────────────────────────────────────────────────────────────────────

// mlxArgs renders the mtplx engine's command line — the only mtplx argv home,
// mirroring LaunchSpec.Args's llama monopoly (ADR-092 D6). `-m mtplx.cli`
// under -I: no `mtplx/__main__.py` exists, and the console script's shebang
// pins the staging path the promote rename invalidates. Shape = the llama
// invariants: loopback bind, the plan's explicit context, no rate limit, and
// the model as the verified pack DIRECTORY — never an HF id.
func (spec LaunchSpec) mlxArgs() []string {
	args := make([]string, 0, 16)
	args = append(args,
		"-I",
		"-m", "mtplx.cli", "serve",
		"--model", spec.ModelFile,
		"--host", spec.Host,
		"--port", strconv.Itoa(spec.Port),
		"--context-window", strconv.Itoa(spec.ContextSize),
		"--rate-limit", "0",
	)
	return args
}

// mlxValidate refuses a spec that must never reach exec: any non-zero
// llama-family field means a llama plan leaked into an mtplx launch — a
// programming error, not an inheritable default (ADR-092 D5).
func (spec LaunchSpec) mlxValidate() error {
	if spec.ModelFile == "" {
		return fmt.Errorf("%w: the model pack directory is required", ErrLaunchSpecInvalid)
	}
	// Absolute in the POSIX dialect or on this host — relative in both could
	// be steered by the launch CWD; either keeps foreign CI fixtures working.
	if !path.IsAbs(spec.ModelFile) && !filepath.IsAbs(spec.ModelFile) {
		return fmt.Errorf("%w: the model pack directory %q must be absolute", ErrLaunchSpecInvalid, spec.ModelFile)
	}
	if spec.Host != LoopbackHost {
		return fmt.Errorf("%w: the bind must be the loopback host, got %q", ErrLaunchSpecInvalid, spec.Host)
	}
	if !usablePort(spec.Port) {
		return fmt.Errorf("%w: port %d is not a usable TCP port", ErrLaunchSpecInvalid, spec.Port)
	}
	// The context is the ONE memory decision this engine launches with; always
	// explicit, bounded by the model's training context.
	if spec.ContextSize <= 0 || spec.ContextSize > maxTrainingContext {
		return fmt.Errorf("%w: context size %d outside (0, %d]", ErrLaunchSpecInvalid, spec.ContextSize, maxTrainingContext)
	}
	if _, emitsLayers := spec.Layers.arg(); emitsLayers {
		return fmt.Errorf("%w: -ngl does not exist on the %s engine", ErrLaunchSpecInvalid, EngineMTPLX)
	}
	if spec.Fit || spec.Parallel != 0 ||
		spec.KVType != "" || spec.MMProjFile != "" || len(spec.Devices) > 0 ||
		spec.SplitMode != SplitModeAuto {
		return fmt.Errorf("%w: llama-family launch fields set on an %s spec (fit=%t, layers=%v, parallel=%d, kv=%q, mmproj=%q)",
			ErrLaunchSpecInvalid, EngineMTPLX, spec.Fit, spec.Layers, spec.Parallel, spec.KVType, spec.MMProjFile)
	}
	return nil
}

// ── launch identity ──────────────────────────────────────────────────────────

// launchSpecMLX turns an mtplx install record into a launch specification.
// Identity (interpreter, pack dir) is containment-checked against the layout.
// Shape is re-derived from the SAME pinned terms with the LIVE tuning (PlanMLX
// is pure), so `context.mode: exact` keeps working (ADR-092 D5): a feasible
// live plan wins, an infeasible one refuses, an unpriceable one (ErrRAMUnknown,
// planner-refused tuning) falls back to the recorded plan.
func (s *Server) launchSpecMLX(manifest Manifest, port int) (resolvedLaunch, error) {
	if manifest.Engine != EngineMTPLX {
		return resolvedLaunch{}, fmt.Errorf("%w: launchSpecMLX called for engine %q", ErrLaunchSpecInvalid, manifest.Engine)
	}
	runtimeDir, err := s.Layout.MLXRuntimeDir()
	if err != nil {
		return resolvedLaunch{}, err
	}
	binary, err := MLXServerPythonPath(runtimeDir)
	if err != nil {
		return resolvedLaunch{}, err
	}
	if !pathExists(binary) {
		return resolvedLaunch{}, fmt.Errorf("%w: the provisioned Python %q is missing — reinstall the model",
			ErrNotInstalled, binary)
	}

	modelFile := manifest.ModelFile
	if modelFile == "" || !s.Layout.OwnsMLXPack(modelFile) {
		derived, deriveErr := s.Layout.MLXPackDir()
		if deriveErr != nil {
			return resolvedLaunch{}, deriveErr
		}
		if modelFile != "" {
			s.logger().Warn("the recorded model directory is outside the MLX pack root; launching the layout-derived path instead",
				"recorded", modelFile, "derived", derived)
		}
		modelFile = derived
	}
	if !pathExists(modelFile) {
		return resolvedLaunch{}, fmt.Errorf("%w: the recorded model directory %q is missing — reinstall the model",
			ErrNotInstalled, modelFile)
	}

	spec := LaunchSpec{
		Engine:       EngineMTPLX,
		ServerBinary: binary,
		ModelFile:    modelFile,
		Host:         LoopbackHost,
		Port:         port,
	}
	plan := manifest.Plan
	terms, err := PinnedMLXMemoryTerms()
	if err != nil {
		// Pack pins vs pinned memory contract — the install-refused build-time
		// drift; a launch must not serve a shape nobody could price.
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	}
	var ramGiB float64
	if manifest.Topology != nil {
		ramGiB = manifest.Topology.HostRAMGiB
	}
	live, err := PlanMLX(ramGiB, s.tuning(), terms)
	switch {
	case err == nil:
		plan = &live
	case errors.Is(err, ErrMemoryPlanInfeasible):
		// The live tuning asks for a window this machine cannot hold; launching
		// the smaller recorded window would silently ignore the override the
		// gate's own advice directed, so the refusal — carrying the gate's
		// arithmetic — is the answer.
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	default:
		// ErrRAMUnknown (a manifest written before the topology snapshot
		// existed) or a tuning the planner refuses (config validation owns
		// that): launch the recorded decision.
		s.logger().Debug("the mtplx launch shape was not re-planned from the live tuning; launching the recorded plan",
			"error", err)
	}
	switch {
	case plan != nil && plan.ContextSize > 0:
		spec.ContextSize = plan.ContextSize
	case plan == nil && manifest.ContextSize > 0:
		// A plan-less manifest: the recorded context tier IS the window.
		spec.ContextSize = manifest.ContextSize
	default:
		return resolvedLaunch{}, fmt.Errorf("%w: the mtplx install record carries no context to launch with",
			ErrLaunchSpecInvalid)
	}
	if err := spec.Validate(); err != nil {
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	}
	return resolvedLaunch{Spec: spec, Plan: plan}, nil
}

// ── child environment ─────────────────────────────────────────────────────────

// mlxChildEnv is the COMPLETE environment of every mtplx python child — pip,
// smoke, server (ADR-092 D1/D3). Scrub PIP_*/PYTHON*/MTPLX_* (foreign wheels,
// import shadowing, standalone MTPLX state, user site-packages must not reach
// pinned bytes) and rebuild with the pinned bin FIRST on PATH,
// PYTHONNOUSERSITE=1, MTPLX_CONFIG/MTPLX_MODEL_DIR pointed at the tree's own
// paths. Set by replace-in-scrub, never append: getenv reads the FIRST
// duplicate of a name, os.environ the last.
//
// pythonBinPath is the pinned interpreter (<tree>/python/bin/python3.12);
// paths stay in the HOST dialect; goos only picks the PATH list separator
// (keeps the table CI-testable).
func mlxChildEnv(parent []string, goos, pythonBinPath string) []string {
	treeRoot := filepath.Dir(filepath.Dir(filepath.Dir(pythonBinPath)))
	env := make([]string, 0, len(parent)+6)
	inheritedPATH := ""
	for _, entry := range parent {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			env = append(env, entry)
			continue
		}
		switch {
		case strings.HasPrefix(name, "PIP_"), strings.HasPrefix(name, "PYTHON"), strings.HasPrefix(name, "MTPLX_"):
			continue // scrubbed: the child's bytes are pinned, its env must be too
		case name == "PATH":
			inheritedPATH = value
			continue // re-added below with the pinned bin dir first
		}
		env = append(env, entry)
	}
	if inheritedPATH == "" {
		env = append(env, "PATH="+filepath.Dir(pythonBinPath))
	} else {
		env = append(env, "PATH="+filepath.Dir(pythonBinPath)+pathListSeparator(goos)+inheritedPATH)
	}
	env = append(env,
		"PYTHONNOUSERSITE=1",
		"MTPLX_CONFIG="+filepath.Join(treeRoot, "config.toml"),
		"MTPLX_MODEL_DIR="+treeRoot, // never used for downloads (path-loaded), but never points at ~/.mtplx either
	)
	return env
}

// ── context readback ─────────────────────────────────────────────────────────

// healthURL is the mtplx engine's state endpoint (llama's /props twin).
func healthURL(port int) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/health"
}

// healthPayload is the /health slice the readback needs: TOP-LEVEL
// `context_window` is what the loaded backend actually serves; the caller
// clamps it to the launched window.
type healthPayload struct {
	ContextWindow *int `json:"context_window"`
}

// readHealthContext asks a READY mtplx server its context, fail-soft like
// readPropsContext — (0, false) for every failure, so the readback never
// fails a load that is already serving.
func readHealthContext(ctx context.Context, client *http.Client, url string, timeout time.Duration) (int, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPropsBodyLen))
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0, false
	}
	var payload healthPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, false
	}
	if payload.ContextWindow == nil || *payload.ContextWindow <= 0 {
		return 0, false
	}
	if *payload.ContextWindow > maxTrainingContext {
		return 0, false
	}
	return *payload.ContextWindow, true
}

// ── failure markers ──────────────────────────────────────────────────────────

// mlxDoesNotFitMarker is the mtplx planner's admission-refusal signature (the
// llama fork never prints it), so the shared tail scanner can carry both
// engines.
const mlxDoesNotFitMarker = "MODEL DOES NOT FIT"

// scanMLXFitFailure renders the actionable message for an mtplx admission
// refusal found in the server's output tail. Empty means "not this failure".
func scanMLXFitFailure(tail string) string {
	if strings.Contains(tail, mlxDoesNotFitMarker) {
		return "the MTPLX planner refused the load as too big for its measured engine budget — " +
			"re-plan (a smaller context.mode) or use a machine with more memory"
	}
	return ""
}
