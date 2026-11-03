package embeddedllm

// The mtplx engine's supervision surface (ADR-092): the argv, the validation,
// the launch identity, the child environment and the /health context
// readback. The supervisor machinery — the single-instance gate, the readiness
// polling loop, crash detection, the idle unload, the stop budgets — is
// runtime-agnostic and lives in server.go untouched; this file is only the
// engine-shaped half.

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

// mlxArgs renders the mtplx engine's command line. This is the only place an
// mtplx argv exists, mirroring LaunchSpec.Args's llama monopoly. The
// interpreter is launched with -I (isolated: no user site-packages, no
// PYTHON* environment influence) and runs the engine through its `mtplx.cli`
// module — verified against the pinned wheel: the distribution ships no
// `mtplx/__main__.py`, so `-m mtplx` cannot work, while cli.py carries the
// `if __name__ == "__main__"` guard `-m mtplx.cli` executes. (The pip-installed
// console script in <tree>/bin is not used because its generated shebang
// points at the staging tree's absolute interpreter path, which the promote
// rename invalidates.) The run shape is fixed by the same invariants as the
// llama argv: the `serve` subcommand with loopback bind, explicit context (the
// plan's computed window), rate limiting off (one local client, one request at
// a time), and the model given as the pack DIRECTORY the installer verified
// (never an HF id, so the runtime can never resolve it to different bytes) —
// `serve` spells the model `--model`; it has no `-m` alias.
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

// mlxValidate refuses a specification that must never reach exec. The llama
// fields (fit, layers, KV precision, slots, the projector) are not part of
// this engine's vocabulary: a non-zero one means a llama plan leaked into an
// mtplx launch, which is a programming error, not a default to inherit.
func (spec LaunchSpec) mlxValidate() error {
	if spec.ModelFile == "" {
		return fmt.Errorf("%w: the model pack directory is required", ErrLaunchSpecInvalid)
	}
	// Absolute in the POSIX dialect or on this host — a path relative in BOTH
	// could be steered by the launch CWD. The engine host is darwin, where the
	// two dialects coincide; accepting either keeps the guard intact on a
	// foreign CI host that feeds POSIX-literal fixtures.
	if !path.IsAbs(spec.ModelFile) && !filepath.IsAbs(spec.ModelFile) {
		return fmt.Errorf("%w: the model pack directory %q must be absolute", ErrLaunchSpecInvalid, spec.ModelFile)
	}
	if spec.Host != LoopbackHost {
		return fmt.Errorf("%w: the bind must be the loopback host, got %q", ErrLaunchSpecInvalid, spec.Host)
	}
	if !usablePort(spec.Port) {
		return fmt.Errorf("%w: port %d is not a usable TCP port", ErrLaunchSpecInvalid, spec.Port)
	}
	// The context is the ONE memory decision this engine launches with; it is
	// always explicit and bounded by the model's own training context.
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
// The identity half is containment-checked against the layout exactly like the
// llama path: the interpreter is derived from the runtime tree, and the
// recorded pack directory must lie inside the MLX pack root (a tampered
// manifest cannot aim the launch at foreign bytes).
//
// The SHAPE half is the plan's context window. The install records the plan it
// made; the launch re-derives it from the SAME pinned terms with the operator
// tuning snapshot launchSpec read once at entry — the same value its
// ForbiddenLlamaTuning pre-check refused on, so pre-check and planner can
// never disagree about what the operator asked (PlanMLX is pure — no probe,
// no I/O; an operator edit saved mid-launch applies to the NEXT launch). An
// infeasible live plan refuses the launch with the gate's own error;
// a plan the launch cannot price at all (no topology snapshot to take the RAM
// from — ErrRAMUnknown) falls back to the recorded plan. A tuning the planner
// refuses fails closed: launchSpec refuses the llama-key contradiction before
// this runs, and anything else that slips past the write-side checks is
// returned, never papered over.
func (s *Server) launchSpecMLX(manifest Manifest, port int, tuning Tuning) (resolvedLaunch, error) {
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
		// The pinned pack pins and the pinned memory contract disagree: this
		// is the same build-time drift the install refuses on, and a launch
		// must not serve a shape nobody was able to price.
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	}
	var ramGiB float64
	if manifest.Topology != nil {
		ramGiB = manifest.Topology.HostRAMGiB
	}
	live, err := PlanMLX(ramGiB, tuning, terms)
	switch {
	case err == nil:
		plan = &live
	case errors.Is(err, ErrMemoryPlanInfeasible):
		// The live operator tuning asks for a window this machine cannot
		// hold. Launching the smaller recorded window would silently ignore
		// the override the gate's advice told the operator to use, so the
		// refusal is the answer — carrying the gate's own arithmetic.
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
	case errors.Is(err, ErrRAMUnknown):
		// A manifest with no topology snapshot — every manifest written
		// before the snapshot existed — cannot be priced at all, which is
		// information the launch lacks rather than an override it would
		// ignore: the recorded decision launches.
		s.logger().Debug("the mtplx launch shape was not re-planned from the live tuning; launching the recorded plan",
			"error", err)
	default:
		// A tuning the planner refuses is fail-closed: launchSpec's
		// ForbiddenLlamaTuning pre-check already refuses the engine-mismatch
		// keys, so reaching here means an invalid override slipped past every
		// write-side check — the recorded plan must not silently replace it.
		return resolvedLaunch{}, fmt.Errorf("embeddedllm: %w", err)
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

// mlxChildEnv is the COMPLETE environment every mtplx python child runs with —
// the offline pip install, the import smoke test and the supervised server.
// The engine children are built from operator-invisible pinned bytes, so they
// must not inherit an operator environment that can steer them: an exported
// PIP_FIND_LINKS would offer pip foreign wheels, PYTHONPATH/PYTHONHOME would
// shadow the pinned imports, an operator MTPLX_CONFIG could mix standalone
// state into the pinned tree, and CPython adds the user site-packages to
// sys.path even with no variable set at all (ADR-092 D1/D3, SECURITY.md
// ASI04). So the parent environment is scrubbed of every PIP_*, PYTHON* and
// MTPLX_* key (case-sensitive — the documented prefixes) and rebuilt:
//
//   - PATH with the pinned tree's bin directory FIRST, so the staged
//     interpreter and its console scripts resolve before anything on the
//     operator's PATH;
//   - PYTHONNOUSERSITE=1, so the user site-packages can never shadow the
//     pinned wheels;
//   - TEMPDIR/TMP pass through untouched (the scrub does not match them):
//     pip and the engine need a writable temp;
//   - MTPLX_CONFIG/MTPLX_MODEL_DIR set EXPLICITLY to the tree's own paths —
//     never inherited, and set once (replace-by-scrub, not append), so no
//     reader — C-level getenv takes the FIRST duplicate, CPython's os.environ
//     the last — can resolve an operator's value (the naive append-to-env
//     hazard).
//
// pythonBinPath is the pinned interpreter the child is launched with
// (<tree>/python/bin/python3.12): its directory seeds PATH and its tree root
// holds the isolated config/model paths.
// Paths in and out are in the HOST dialect — production feeds the real
// staged-tree's paths, so the derivation uses filepath. The goos argument's
// only decision is the PATH list separator (see pathListSeparator); that
// split is what keeps the child-env table testable from any CI machine, with
// fixtures built through the same host filepath.
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

// healthPayload is the slice of the /health response the context readback
// needs. The TOP-LEVEL `context_window` is the window the loaded backend is
// actually serving (verified against a live server: it mirrors the launched
// `--context-window`, and `/execution_window.basis` reads
// "configured_window"); the readback takes it and the caller clamps to the
// launched window.
type healthPayload struct {
	ContextWindow *int `json:"context_window"`
}

// readHealthContext asks a READY mtplx server what context it came up with.
// Fail-soft exactly like readPropsContext: (0, false) for every way the
// question can go wrong, because the answer refines a recorded value and must
// never become the reason a load that is already serving reports failure.
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

// mlxDoesNotFitMarker is the mtplx planner's admission refusal signature: a
// load that cannot fit gets the smallest window and a line that says so. The
// marker is engine-agnostic by construction — the llama fork never prints it —
// so the shared tail scanner can carry both.
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
